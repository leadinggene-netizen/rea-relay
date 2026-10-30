// Package connection models connections, subscriptions/licensing, symbol maps
// and accounts (spec §4, FR-17..20). Keys are portable, concurrency is bounded
// per tier, and expiry is evaluated on a UTC day boundary.
package connection

import (
	"strings"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// BrokerSymbolSpec describes a broker instrument: its mapped label plus the
// volume constraints and valuation constants the relay needs for server-side
// risk math (spec §6, FR-14).
type BrokerSymbolSpec struct {
	BrokerSymbol   string  // the broker's instrument label
	PipSize        float64 // price movement of one pip (e.g. 0.0001)
	PipValuePerLot float64 // cash value of one pip for one lot (V in §6)
	LotMin         float64 // broker minimum volume
	LotMax         float64 // broker maximum volume
	LotStep        float64 // broker lot step for rounding
}

// Account is a trading account reachable through a connection (spec §4). Equity
// backs the account_filter gate; Mode is demo/live (portability, FR-18).
type Account struct {
	ID         string
	Broker     string
	LoginLabel string
	Mode       string // "demo" or "live"
	Equity     float64
}

// Connection authorizes one concurrently-connected account and carries its auth
// material, symbol table and expiry (spec §4, FR-2/3, FR-17/18/20).
type Connection struct {
	ID               string
	Key              string
	Secret           string // shared secret for HMAC; empty if signatures not required
	RequireSignature bool
	SubscriptionID   string
	ExpiryUTC        time.Time // access ends at the START of this day (UTC)
	SymbolTable      map[string]BrokerSymbolSpec
	Accounts         []Account
}

// Subscription grants a tier's concurrent-connection budget (spec §4, FR-17).
type Subscription struct {
	ID             string
	Tier           string
	MaxConnections int
}

// placeholderKeys are obvious not-yet-replaced values treated as a missing key
// (spec EC-1). Assumption documented in BUILD-REPORT.md.
var placeholderKeys = map[string]bool{
	"replace_me": true, "your_key": true, "your_connection_key": true,
	"changeme": true, "placeholder": true, "xxxx": true,
}

// IsPlaceholderKey reports whether a presented key is blank or an obvious
// placeholder (EC-1 -> missing_key).
func IsPlaceholderKey(k string) bool {
	t := strings.ToLower(strings.TrimSpace(k))
	if t == "" {
		return true
	}
	return placeholderKeys[t]
}

// Expired reports whether the connection has expired as of now. Access ends at
// the start of the expiry day in UTC (FR-20, EC-15): the cutoff is midnight UTC
// of ExpiryUTC's calendar day.
func (c *Connection) Expired(now time.Time) bool {
	if c.ExpiryUTC.IsZero() {
		return false
	}
	y, m, d := c.ExpiryUTC.UTC().Date()
	cutoff := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return !now.UTC().Before(cutoff)
}

// MapSymbol resolves a source symbol to its broker spec. Matching is
// case-sensitive on the exact label (EC-2: a case-mismatch is unmapped).
func (c *Connection) MapSymbol(source string) (BrokerSymbolSpec, bool) {
	spec, ok := c.SymbolTable[source]
	return spec, ok
}

// Store is an in-memory registry of connections and subscriptions plus a live
// count of connected agents per subscription for concurrency enforcement
// (FR-17, EC-14). It is safe for concurrent use.
type Store struct {
	mu            sync.Mutex
	byKey         map[string]*Connection
	byID          map[string]*Connection
	subscriptions map[string]*Subscription
	// connected tracks currently-connected connection IDs per subscription.
	connected map[string]map[string]bool
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		byKey:         map[string]*Connection{},
		byID:          map[string]*Connection{},
		subscriptions: map[string]*Subscription{},
		connected:     map[string]map[string]bool{},
	}
}

// AddSubscription registers a subscription/plan.
func (s *Store) AddSubscription(sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[sub.ID] = sub
}

// AddConnection registers a connection.
func (s *Store) AddConnection(c *Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[c.Key] = c
	s.byID[c.ID] = c
}

// Lookup finds a connection by its presented key (FR-2). Keys are portable, so
// the same key is accepted regardless of account (FR-18).
func (s *Store) Lookup(key string) (*Connection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byKey[key]
	return c, ok
}

// Subscription returns the subscription for an id.
func (s *Store) Subscription(id string) (*Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subscriptions[id]
	return sub, ok
}

// Connect registers a connection as currently connected, enforcing the tier's
// concurrency limit (FR-17, EC-14, TC-X4). If the connection is already marked
// connected it is idempotent and allowed. Returns connection_limit otherwise.
func (s *Store) Connect(connID string) (reason.Code, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[connID]
	if !ok {
		return reason.UnknownKey, false
	}
	sub, ok := s.subscriptions[c.SubscriptionID]
	if !ok {
		return reason.UnknownKey, false
	}
	set := s.connected[sub.ID]
	if set == nil {
		set = map[string]bool{}
		s.connected[sub.ID] = set
	}
	if set[connID] {
		return "", true // already connected: idempotent
	}
	if len(set) >= sub.MaxConnections {
		return reason.ConnectionLimit, false
	}
	set[connID] = true
	return "", true
}

// Disconnect marks a connection as no longer connected, freeing a slot.
func (s *Store) Disconnect(connID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[connID]
	if !ok {
		return
	}
	if set := s.connected[c.SubscriptionID]; set != nil {
		delete(set, connID)
	}
}

// ConnectedCount returns the number of currently-connected connections for a
// subscription (test/observability helper).
func (s *Store) ConnectedCount(subID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.connected[subID])
}
