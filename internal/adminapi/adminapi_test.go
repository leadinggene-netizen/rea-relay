package adminapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leadinggene-netizen/rea-relay/internal/adminapi"
	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
	"github.com/leadinggene-netizen/rea-relay/internal/risk"
	filestore "github.com/leadinggene-netizen/rea-relay/internal/store"
)

const adminToken = "s3cret-admin-token"

// do issues a request against the API handler with an optional bearer token and
// JSON body, returning the recorded response.
func do(t *testing.T, api *adminapi.API, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func subBody(id, tier string, max int, owner string) map[string]any {
	return map[string]any{"id": id, "tier": tier, "max_connections": max, "owner_id": owner}
}

// connBody builds a connection create payload with a one-symbol table and one
// account, matching the demo seed's shape.
func connBody(id, key, sub string) map[string]any {
	return map[string]any{
		"id": id, "key": key, "subscription_id": sub,
		"symbol_table": map[string]any{
			"XAUUSD": map[string]any{
				"broker_symbol": "XAUUSD", "pip_size": 0.1, "pip_value_per_lot": 1.0,
				"lot_min": 0.01, "lot_max": 100.0, "lot_step": 0.01,
			},
		},
		"accounts": []map[string]any{
			{"id": "acct-1", "broker": "demo", "mode": "demo", "equity": 10000.0},
		},
	}
}

// TestDisabledWhenNoToken: with no token configured, every admin route is 404.
func TestDisabledWhenNoToken(t *testing.T) {
	api := adminapi.New(connection.NewStore(), "")
	if api.Enabled() {
		t.Fatal("API with empty token should report disabled")
	}
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/subscriptions"},
		{http.MethodPost, "/admin/connections"},
		{http.MethodGet, "/admin/connections/x"},
	} {
		w := do(t, api, tc.method, tc.path, adminToken, nil) // even WITH a bearer
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: want 404 when disabled, got %d", tc.method, tc.path, w.Code)
		}
	}
}

// TestAuth: wrong/missing bearer -> 401, correct -> 200.
func TestAuth(t *testing.T) {
	api := adminapi.New(connection.NewStore(), adminToken)
	if !api.Enabled() {
		t.Fatal("API with a token should report enabled")
	}
	if w := do(t, api, http.MethodGet, "/admin/subscriptions", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer: want 401, got %d", w.Code)
	}
	if w := do(t, api, http.MethodGet, "/admin/subscriptions", "wrong-token", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: want 401, got %d", w.Code)
	}
	if w := do(t, api, http.MethodGet, "/admin/subscriptions", adminToken, nil); w.Code != http.StatusOK {
		t.Fatalf("correct bearer: want 200, got %d", w.Code)
	}
}

// TestCreateAndWebhookEndToEnd: a subscription + connection created via the
// admin API is immediately usable — a /webhook (through the relay) with the new
// connection key succeeds, because the admin writes go to the live store.
func TestCreateAndWebhookEndToEnd(t *testing.T) {
	store := connection.NewStore()
	api := adminapi.New(store, adminToken)

	if w := do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sub-x", "pro", 3, "tenant-1")); w.Code != http.StatusOK {
		t.Fatalf("create subscription: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("conn-x", "key-x", "sub-x")); w.Code != http.StatusOK {
		t.Fatalf("create connection: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	// Build a relay over the same live store and attach an agent for the new
	// connection so a webhook can be delivered.
	quotes := risk.MapQuoteSource{"XAUUSD": {Bid: 2400.0, Ask: 2400.2, Spread: 0.2}}
	r := relay.New(relay.DefaultConfig(), store, quotes, relay.NewAuditLog())
	if _, err := r.RegisterAgent("conn-x", delivery.NewMockAgent(0.01)); err != nil {
		t.Fatalf("register agent for admin-created connection: %v", err)
	}

	wh := map[string]any{
		"schema_version": 1, "connection_key": "key-x",
		"action": "open_long", "symbol": "XAUUSD",
		"sizing":    map[string]any{"mode": "fixed_lots", "value": 0.10},
		"stop_loss": 20.0, "stop_type": "pips",
	}
	b, _ := json.Marshal(wh)
	res := r.Process(relay.Request{Body: b})
	if !res.Accepted || res.Overall != delivery.StatusFilled {
		t.Fatalf("webhook via admin-created connection: want accepted/filled, got accepted=%v overall=%s code=%s",
			res.Accepted, res.Overall, res.Code)
	}
}

// TestMaxConnectionsEnforced: creating one connection past the tier limit -> 409.
func TestMaxConnectionsEnforced(t *testing.T) {
	store := connection.NewStore()
	api := adminapi.New(store, adminToken)

	do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sub-1", "basic", 1, ""))

	if w := do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("c1", "k1", "sub-1")); w.Code != http.StatusOK {
		t.Fatalf("first connection: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	w := do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("c2", "k2", "sub-1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("second connection past limit: want 409, got %d (%s)", w.Code, w.Body.String())
	}
	var eb struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &eb)
	if eb.Error != "connection_limit" {
		t.Fatalf("want error=connection_limit, got %q", eb.Error)
	}

	// Re-POSTing an existing connection ID (update in place) must NOT be blocked
	// by the limit.
	if w := do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("c1", "k1b", "sub-1")); w.Code != http.StatusOK {
		t.Fatalf("update-in-place of existing connection: want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestDeleteStopsAuthenticating: after DELETE, a webhook with the deleted key is
// rejected as unknown_key.
func TestDeleteStopsAuthenticating(t *testing.T) {
	store := connection.NewStore()
	api := adminapi.New(store, adminToken)
	do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sub-1", "pro", 3, ""))
	do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("conn-d", "key-d", "sub-1"))

	if _, ok := store.Lookup("key-d"); !ok {
		t.Fatal("connection should be present before delete")
	}
	if w := do(t, api, http.MethodDelete, "/admin/connections/conn-d", adminToken, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d (%s)", w.Code, w.Body.String())
	}
	if _, ok := store.Lookup("key-d"); ok {
		t.Fatal("connection should be gone from the live store after delete")
	}

	// Through the relay, the deleted key now authenticates as unknown_key.
	r := relay.New(relay.DefaultConfig(), store, risk.MapQuoteSource{}, relay.NewAuditLog())
	wh := map[string]any{
		"schema_version": 1, "connection_key": "key-d",
		"action": "open_long", "symbol": "XAUUSD",
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	}
	b, _ := json.Marshal(wh)
	res := r.Process(relay.Request{Body: b})
	if res.Code != "unknown_key" {
		t.Fatalf("webhook after delete: want unknown_key, got %q (accepted=%v)", res.Code, res.Accepted)
	}
}

// TestWriteThroughPersistence: a connection created via the admin API is written
// through the persister and survives reopening the store from disk.
func TestWriteThroughPersistence(t *testing.T) {
	dir := t.TempDir()
	st, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store := connection.NewStore()
	store.SetPersister(st)

	api := adminapi.New(store, adminToken)
	do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sub-p", "pro", 3, "tenant-9"))
	if w := do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("conn-p", "key-p", "sub-p")); w.Code != http.StatusOK {
		t.Fatalf("create connection: %d (%s)", w.Code, w.Body.String())
	}
	st.Close()

	// Reopen from disk into a fresh store and confirm both survive.
	st2, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()
	conns, subs, err := st2.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "sub-p" || subs[0].OwnerID != "tenant-9" {
		t.Fatalf("subscription not persisted correctly: %+v", subs)
	}
	var found *connection.Connection
	for _, c := range conns {
		if c.ID == "conn-p" {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("admin-created connection not persisted; got %d connections", len(conns))
	}
	if found.Key != "key-p" || len(found.SymbolTable) != 1 || len(found.Accounts) != 1 {
		t.Fatalf("persisted connection malformed: %+v", found)
	}
}

// TestListAndFilters exercises list endpoints and the subscription_id / owner_id
// filters.
func TestListAndFilters(t *testing.T) {
	store := connection.NewStore()
	api := adminapi.New(store, adminToken)
	do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sA", "pro", 5, "owner-1"))
	do(t, api, http.MethodPost, "/admin/subscriptions", adminToken, subBody("sB", "pro", 5, "owner-2"))
	do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("a1", "ka1", "sA"))
	do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("a2", "ka2", "sA"))
	do(t, api, http.MethodPost, "/admin/connections", adminToken, connBody("b1", "kb1", "sB"))

	count := func(path string) int {
		w := do(t, api, http.MethodGet, path, adminToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
		var arr []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
			t.Fatalf("decode list %s: %v", path, err)
		}
		return len(arr)
	}
	if n := count("/admin/connections"); n != 3 {
		t.Fatalf("all connections: want 3, got %d", n)
	}
	if n := count("/admin/connections?subscription_id=sA"); n != 2 {
		t.Fatalf("filter sub=sA: want 2, got %d", n)
	}
	if n := count("/admin/connections?owner_id=owner-2"); n != 1 {
		t.Fatalf("filter owner=owner-2: want 1, got %d", n)
	}
	if n := count("/admin/subscriptions"); n != 2 {
		t.Fatalf("subscriptions: want 2, got %d", n)
	}

	// Single fetch + not-found.
	if w := do(t, api, http.MethodGet, "/admin/connections/a1", adminToken, nil); w.Code != http.StatusOK {
		t.Fatalf("get a1: want 200, got %d", w.Code)
	}
	if w := do(t, api, http.MethodGet, "/admin/connections/nope", adminToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("get missing: want 404, got %d", w.Code)
	}
}
