// Command relay is a runnable HTTP ingress for the v1 relay backend. It wires
// the in-memory stores, a demo connection, a mock terminal agent, and the
// relay orchestrator behind a single webhook endpoint (FR-1). This is a
// development entrypoint, not a production server: all state is in-memory.
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
	"github.com/leadinggene-netizen/rea-relay/internal/transport"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// In-memory wiring with one demo connection/account and a mock agent.
	store := connection.NewStore()
	store.AddSubscription(&connection.Subscription{ID: "sub-demo", Tier: "pro", MaxConnections: 3})
	demo := &connection.Connection{
		ID:             "conn-demo",
		Key:            "demo-connection-key",
		SubscriptionID: "sub-demo",
		SymbolTable: map[string]connection.BrokerSymbolSpec{
			"XAUUSD": {BrokerSymbol: "XAUUSD", PipSize: 0.1, PipValuePerLot: 1.0, LotMin: 0.01, LotMax: 100, LotStep: 0.01},
		},
		Accounts: []connection.Account{{ID: "acct-1", Broker: "demo", Mode: "demo", Equity: 10000}},
	}
	store.AddConnection(demo)

	quotes := risk.MapQuoteSource{
		"XAUUSD": {Bid: 2400.0, Ask: 2400.2, Spread: 0.2},
	}

	audit := relay.NewAuditLog()
	r := relay.New(relay.DefaultConfig(), store, quotes, audit)

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
