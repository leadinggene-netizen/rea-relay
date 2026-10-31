// Command relay is a runnable HTTP ingress for the v1 relay backend. It wires
// the connection/subscription registry, a demo connection, a mock terminal
// agent, and the relay orchestrator behind a single webhook endpoint (FR-1).
// Durable state (connections/subscriptions, idempotency keys, the audit trail)
// is file-backed under -data-dir and survives a restart; in-flight delivery is
// not persisted (see internal/store/README.md). This is a development
// entrypoint, not a production server.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/auth"
	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
	"github.com/leadinggene-netizen/rea-relay/internal/risk"
	filestore "github.com/leadinggene-netizen/rea-relay/internal/store"
	"github.com/leadinggene-netizen/rea-relay/internal/transport"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dataDir := flag.String("data-dir", "./data", "directory for durable file-backed state")
	flag.Parse()

	cfg := relay.DefaultConfig()

	// Durable, file-backed persistence (stdlib only). Opens/creates the data
	// directory and loads any previously persisted state.
	st, err := filestore.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Load the connection/subscription registry from disk, seeding the demo
	// connection only when the data dir is empty (a fresh install). The
	// persister is attached AFTER load/seed so the initial population does not
	// trigger redundant write-backs.
	store := connection.NewStore()
	seeded, err := loadOrSeed(st, store)
	if err != nil {
		log.Fatalf("load/seed config: %v", err)
	}
	if seeded {
		log.Printf("seeded demo connection into fresh data dir %s", *dataDir)
	}
	store.SetPersister(st)

	quotes := risk.MapQuoteSource{
		"XAUUSD": {Bid: 2400.0, Ask: 2400.2, Spread: 0.2},
		"BTCUSD": {Bid: 95000.0, Ask: 95050.0, Spread: 50.0},
	}

	audit := relay.NewAuditLog()
	audit.SetPersister(st) // append-only audit.jsonl
	r := relay.New(cfg, store, quotes, audit)
	// Make idempotency durable: surviving keys suppress replayed webhooks after
	// a restart; aged-out keys are pruned on load and periodically thereafter.
	if err := r.AttachDedupStore(st); err != nil {
		log.Fatalf("attach dedup store: %v", err)
	}
	go func() {
		ticker := time.NewTicker(cfg.DedupWindow)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := st.PruneDedup(cfg.DedupWindow, time.Now()); err != nil {
				log.Printf("prune dedup: %v", err)
			}
		}
	}()

	// Real transport (§13): the terminal agent connects over WebSocket (primary)
	// or the long-poll fallback and is registered with the relay on connect.
	ts := transport.NewServer(r, store, audit, transport.Config{})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	ts.Routes(mux) // /agent/ws (TR-1..6) and /agent/longpoll (TR-7)
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		rr := relay.Request{Body: body}
		// Optional signature headers (NFR-3).
		if sig := req.Header.Get("X-Signature"); sig != "" {
			ts, _ := strconv.ParseInt(req.Header.Get("X-Timestamp"), 10, 64)
			rr.Signature = &auth.SignatureMaterial{
				Signature: sig,
				Timestamp: ts,
				Nonce:     req.Header.Get("X-Nonce"),
				Body:      body,
			}
		}
		res := r.Process(rr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatusFor(res))
		_ = json.NewEncoder(w).Encode(res)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("relay ingress listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

// loadOrSeed populates store from the persisted config in st, seeding the
// built-in demo connection only when no connections are persisted yet (a fresh
// data dir). On a non-empty data dir the persisted config is used verbatim and
// nothing is re-seeded. It returns whether it seeded. The caller attaches the
// persister afterward so this initial population is not written back redundantly.
func loadOrSeed(st *filestore.FileStore, store *connection.Store) (bool, error) {
	conns, subs, err := st.LoadConfig()
	if err != nil {
		return false, err
	}
	for _, sub := range subs {
		store.AddSubscription(sub)
	}
	for _, c := range conns {
		store.AddConnection(c)
	}
	if len(conns) > 0 {
		return false, nil
	}
	store.AddSubscription(demoSubscription())
	store.AddConnection(demoConnection())
	// Persist the seed exactly once, before the persister is attached.
	seedConns, seedSubs := store.Snapshot()
	if err := st.SaveConfig(seedConns, seedSubs); err != nil {
		return false, err
	}
	return true, nil
}

// demoSubscription returns the built-in demo subscription (migrated from the
// original hardcoded wiring).
func demoSubscription() *connection.Subscription {
	return &connection.Subscription{ID: "sub-demo", Tier: "pro", MaxConnections: 3}
}

// demoConnection returns the built-in demo connection with its XAUUSD + BTCUSD
// symbol table and single demo account (migrated from the original hardcoded
// wiring).
func demoConnection() *connection.Connection {
	return &connection.Connection{
		ID:             "conn-demo",
		Key:            "demo-connection-key",
		SubscriptionID: "sub-demo",
		SymbolTable: map[string]connection.BrokerSymbolSpec{
			"XAUUSD": {BrokerSymbol: "XAUUSD", PipSize: 0.1, PipValuePerLot: 1.0, LotMin: 0.01, LotMax: 100, LotStep: 0.01},
			"BTCUSD": {BrokerSymbol: "BTCUSD", PipSize: 1.0, PipValuePerLot: 1.0, LotMin: 0.01, LotMax: 100, LotStep: 0.01},
		},
		Accounts: []connection.Account{{ID: "acct-1", Broker: "demo", Mode: "demo", Equity: 10000}},
	}
}

// httpStatusFor maps a relay Result to an HTTP status: auth failures -> 401,
// malformed/schema -> 400, everything else -> 200 (acknowledged with a reason).
func httpStatusFor(res relay.Result) int {
	switch res.Code {
	case "missing_key", "unknown_key", "expired_key", "bad_signature":
		return http.StatusUnauthorized
	case "malformed_payload", "schema_version_unsupported":
		return http.StatusBadRequest
	default:
		return http.StatusOK
	}
}
