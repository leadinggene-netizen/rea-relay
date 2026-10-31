// Package adminapi is the runtime provisioning surface for the hosted,
// multi-tenant relay. A provisioning service uses it to register and manage
// each customer's subscriptions and connections at runtime, instead of relying
// on the single hardcoded demo connection seeded at startup.
//
// # Auth model
//
// The admin API is protected by a single static admin bearer token, supplied
// out of band (the -admin-token flag or REA_ADMIN_TOKEN env in cmd/relay). When
// no token is configured the API is DISABLED: every /admin route answers 404,
// so the surface can never be reached unauthenticated. When a token is set,
// each request must carry "Authorization: Bearer <token>"; the comparison uses
// crypto/subtle.ConstantTimeCompare to avoid leaking the token through timing.
// A missing or wrong token answers 401. This is coarse, single-principal auth:
// the holder is trusted to provision any tenant. Per-tenant scoping is modeled
// by Subscription.OwnerID, not enforced at this layer.
//
// # Endpoints (all JSON)
//
//	POST   /admin/subscriptions        create/update a subscription
//	                                    {id, tier, max_connections, owner_id}
//	GET    /admin/subscriptions         list subscriptions
//	GET    /admin/subscriptions/{id}    fetch one subscription
//	POST   /admin/connections           create/update a connection
//	                                    {id, key, subscription_id, symbol_table,
//	                                     accounts, expiry, ...}
//	GET    /admin/connections           list connections
//	                                    (optional ?subscription_id= or ?owner_id=)
//	GET    /admin/connections/{id}       fetch one connection
//	DELETE /admin/connections/{id}       remove a connection
//
// All writes go through the connection.Store, which is the live in-memory
// registry the relay and transport read from AND (when a persister is attached)
// the durable write-through backing. So a created connection authenticates
// immediately on /webhook and /agent/*, a deleted one immediately stops, and
// both outcomes survive a restart.
//
// Creating a connection enforces its subscription's max_connections: if the
// subscription already holds that many connections, a create of a new
// connection is rejected with 409. Re-POSTing an existing connection ID (an
// update in place) does not count against the limit.
package adminapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
)

// API is the admin HTTP handler over a live connection.Store. Construct it with
// New and mount it with Register (or use it directly as an http.Handler).
type API struct {
	store *connection.Store
	token string
	mux   *http.ServeMux
}

// New builds an admin API over store, authenticated by token. An empty token
// disables the API: every route answers 404 (see package docs). The returned
// *API is an http.Handler rooted at /admin/.
func New(store *connection.Store, token string) *API {
	a := &API{store: store, token: token, mux: http.NewServeMux()}
	a.mux.HandleFunc("POST /admin/subscriptions", a.createSubscription)
	a.mux.HandleFunc("GET /admin/subscriptions", a.listSubscriptions)
	a.mux.HandleFunc("GET /admin/subscriptions/{id}", a.getSubscription)
	a.mux.HandleFunc("POST /admin/connections", a.createConnection)
	a.mux.HandleFunc("GET /admin/connections", a.listConnections)
	a.mux.HandleFunc("GET /admin/connections/{id}", a.getConnection)
	a.mux.HandleFunc("DELETE /admin/connections/{id}", a.deleteConnection)
	return a
}

// Enabled reports whether a token is configured (and thus the API is live).
func (a *API) Enabled() bool { return a.token != "" }

// Register mounts the admin API under /admin/ on mux. Callers typically only
// call this when a token is configured; the handler self-gates regardless.
func (a *API) Register(mux *http.ServeMux) { mux.Handle("/admin/", a) }

// ServeHTTP gates on the auth model, then dispatches. A disabled API (no token)
// answers 404 for every path so its existence is not revealed.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.token == "" {
		http.NotFound(w, r)
		return
	}
	if !a.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin bearer token")
		return
	}
	a.mux.ServeHTTP(w, r)
}

// authorized reports whether r carries the expected "Bearer <token>" header,
// compared in constant time.
func (a *API) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	got := h[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1
}

// --- wire shapes ------------------------------------------------------------

type subscriptionDTO struct {
	ID             string `json:"id"`
	Tier           string `json:"tier"`
	MaxConnections int    `json:"max_connections"`
	OwnerID        string `json:"owner_id,omitempty"`
}

func subscriptionToDTO(s *connection.Subscription) subscriptionDTO {
	return subscriptionDTO{ID: s.ID, Tier: s.Tier, MaxConnections: s.MaxConnections, OwnerID: s.OwnerID}
}

type brokerSymbolSpecDTO struct {
	BrokerSymbol   string  `json:"broker_symbol"`
	PipSize        float64 `json:"pip_size"`
	PipValuePerLot float64 `json:"pip_value_per_lot"`
	LotMin         float64 `json:"lot_min"`
	LotMax         float64 `json:"lot_max"`
	LotStep        float64 `json:"lot_step"`
}

type accountDTO struct {
	ID         string  `json:"id"`
	Broker     string  `json:"broker,omitempty"`
	LoginLabel string  `json:"login_label,omitempty"`
	Mode       string  `json:"mode,omitempty"`
	Equity     float64 `json:"equity,omitempty"`
}

type connectionDTO struct {
	ID               string                         `json:"id"`
	Key              string                         `json:"key"`
	SubscriptionID   string                         `json:"subscription_id"`
	Secret           string                         `json:"secret,omitempty"`
	RequireSignature bool                           `json:"require_signature,omitempty"`
	Expiry           *time.Time                     `json:"expiry,omitempty"`
	SymbolTable      map[string]brokerSymbolSpecDTO `json:"symbol_table,omitempty"`
	Accounts         []accountDTO                   `json:"accounts,omitempty"`
}

func connectionToDTO(c *connection.Connection) connectionDTO {
	dto := connectionDTO{
		ID:               c.ID,
		Key:              c.Key,
		SubscriptionID:   c.SubscriptionID,
		Secret:           c.Secret,
		RequireSignature: c.RequireSignature,
		SymbolTable:      map[string]brokerSymbolSpecDTO{},
	}
	if !c.ExpiryUTC.IsZero() {
		e := c.ExpiryUTC
		dto.Expiry = &e
	}
	for src, spec := range c.SymbolTable {
		dto.SymbolTable[src] = brokerSymbolSpecDTO{
			BrokerSymbol:   spec.BrokerSymbol,
			PipSize:        spec.PipSize,
			PipValuePerLot: spec.PipValuePerLot,
			LotMin:         spec.LotMin,
			LotMax:         spec.LotMax,
			LotStep:        spec.LotStep,
		}
	}
	for _, acct := range c.Accounts {
		dto.Accounts = append(dto.Accounts, accountDTO{
			ID: acct.ID, Broker: acct.Broker, LoginLabel: acct.LoginLabel,
			Mode: acct.Mode, Equity: acct.Equity,
		})
	}
	return dto
}

func (dto connectionDTO) toConnection() *connection.Connection {
	c := &connection.Connection{
		ID:               dto.ID,
		Key:              dto.Key,
		SubscriptionID:   dto.SubscriptionID,
		Secret:           dto.Secret,
		RequireSignature: dto.RequireSignature,
		SymbolTable:      map[string]connection.BrokerSymbolSpec{},
	}
	if dto.Expiry != nil {
		c.ExpiryUTC = *dto.Expiry
	}
	for src, spec := range dto.SymbolTable {
		c.SymbolTable[src] = connection.BrokerSymbolSpec{
			BrokerSymbol:   spec.BrokerSymbol,
			PipSize:        spec.PipSize,
			PipValuePerLot: spec.PipValuePerLot,
			LotMin:         spec.LotMin,
			LotMax:         spec.LotMax,
			LotStep:        spec.LotStep,
		}
	}
	for _, acct := range dto.Accounts {
		c.Accounts = append(c.Accounts, connection.Account{
			ID: acct.ID, Broker: acct.Broker, LoginLabel: acct.LoginLabel,
			Mode: acct.Mode, Equity: acct.Equity,
		})
	}
	return c
}

// --- subscription handlers --------------------------------------------------

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) {
	var dto subscriptionDTO
	if !decode(w, r, &dto) {
		return
	}
	if dto.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "id is required")
		return
	}
	if dto.MaxConnections < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "max_connections must not be negative")
		return
	}
	a.store.AddSubscription(&connection.Subscription{
		ID: dto.ID, Tier: dto.Tier, MaxConnections: dto.MaxConnections, OwnerID: dto.OwnerID,
	})
	sub, _ := a.store.Subscription(dto.ID)
	writeJSON(w, http.StatusOK, subscriptionToDTO(sub))
}

func (a *API) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	subs := a.store.ListSubscriptions()
	out := make([]subscriptionDTO, 0, len(subs))
	for _, s := range subs {
		out = append(out, subscriptionToDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, ok := a.store.Subscription(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no subscription with that id")
		return
	}
	writeJSON(w, http.StatusOK, subscriptionToDTO(sub))
}

// --- connection handlers ----------------------------------------------------

func (a *API) createConnection(w http.ResponseWriter, r *http.Request) {
	var dto connectionDTO
	if !decode(w, r, &dto) {
		return
	}
	if dto.ID == "" || dto.Key == "" || dto.SubscriptionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "id, key and subscription_id are required")
		return
	}
	if connection.IsPlaceholderKey(dto.Key) {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is blank or a placeholder")
		return
	}
	sub, ok := a.store.Subscription(dto.SubscriptionID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "subscription_id does not refer to a known subscription")
		return
	}
	// Enforce the tier's connection budget. Re-POSTing an existing connection ID
	// (update in place) within the same subscription does not consume a new slot.
	existing, exists := a.store.ConnectionByID(dto.ID)
	isNewToSub := !exists || existing.SubscriptionID != dto.SubscriptionID
	if isNewToSub && a.store.CountConnectionsForSubscription(dto.SubscriptionID) >= sub.MaxConnections {
		writeError(w, http.StatusConflict, "connection_limit",
			"subscription is at its max_connections limit")
		return
	}
	a.store.AddConnection(dto.toConnection())
	created, _ := a.store.ConnectionByID(dto.ID)
	writeJSON(w, http.StatusOK, connectionToDTO(created))
}

func (a *API) listConnections(w http.ResponseWriter, r *http.Request) {
	subFilter := r.URL.Query().Get("subscription_id")
	ownerFilter := r.URL.Query().Get("owner_id")

	// Resolve owner_id -> the set of subscription IDs it owns.
	var ownerSubs map[string]bool
	if ownerFilter != "" {
		ownerSubs = map[string]bool{}
		for _, s := range a.store.ListSubscriptions() {
			if s.OwnerID == ownerFilter {
				ownerSubs[s.ID] = true
			}
		}
	}

	conns := a.store.ListConnections()
	out := make([]connectionDTO, 0, len(conns))
	for _, c := range conns {
		if subFilter != "" && c.SubscriptionID != subFilter {
			continue
		}
		if ownerSubs != nil && !ownerSubs[c.SubscriptionID] {
			continue
		}
		out = append(out, connectionToDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := a.store.ConnectionByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no connection with that id")
		return
	}
	writeJSON(w, http.StatusOK, connectionToDTO(c))
}

func (a *API) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.ConnectionByID(id); !ok {
		writeError(w, http.StatusNotFound, "not_found", "no connection with that id")
		return
	}
	a.store.RemoveConnection(id)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ----------------------------------------------------------------

// decode reads a JSON body into v, rejecting unknown fields and oversized
// bodies. It writes a 400 and returns false on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON for this endpoint: "+err.Error())
		return false
	}
	return true
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
