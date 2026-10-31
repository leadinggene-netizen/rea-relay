// Package store provides durable, file-backed persistence for the relay's
// long-lived state so that it survives a process restart. It deliberately uses
// only the Go standard library (no SQL driver, no third-party package): the Go
// module proxy is unavailable in the build/runtime environment, so go.mod must
// stay dependency-free.
//
// Three categories of state are persisted (see README.md for the full layout):
//
//   - Config (subscriptions + connections): a single JSON snapshot written
//     atomically via a temp file + os.Rename, loaded once on startup and
//     rewritten on every change (write-through).
//   - Idempotency / dedup keys (FR-4): a compact JSON map of "connID\x00key"
//     to first-seen time, pruned by the dedup window so a replayed webhook
//     within the window is not re-executed after a reboot.
//   - Audit events (§8): an append-only JSONL file (audit.jsonl), flushed to
//     disk on every write, doubling as a durable operational record.
//
// Deliberately NOT persisted: the in-flight long-poll/WebSocket delivery queue
// (orders mid-flight). On restart those are lost, which is acceptable because
// idempotency prevents double-execution and the upstream (TradingView) re-fires
// the alert. See README.md.
package store

import (
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
)

// AuditRecord is one persisted audit line. Code is kept as a plain string so
// the store package does not depend on internal/reason; callers convert from
// reason.Code (itself a string) at the boundary.
type AuditRecord struct {
	ConnectionID string    `json:"connection_id"`
	Code         string    `json:"code"`
	Detail       string    `json:"detail"`
	At           time.Time `json:"at"`
}

// Store is the durable persistence contract for the relay. The file-backed
// implementation (FileStore) is the only one shipped today; the interface
// exists so call sites depend on behaviour, not on the file format, and so a
// future backend could replace it without touching the relay.
//
// Its method set is a superset of the small persister interfaces the other
// packages declare, so a *FileStore satisfies all of them at once:
//   - connection.ConfigPersister (SaveConfig)
//   - auth.DedupPersister        (LoadDedup, RecordDedup)
//   - relay.AuditPersister       (AppendAudit)
type Store interface {
	// LoadConfig returns the persisted connections and subscriptions. A fresh
	// data dir returns empty slices and a nil error.
	LoadConfig() (conns []*connection.Connection, subs []*connection.Subscription, err error)
	// SaveConfig atomically writes the full config snapshot.
	SaveConfig(conns []*connection.Connection, subs []*connection.Subscription) error

	// LoadDedup returns the persisted idempotency map (combined key -> first-seen).
	LoadDedup() (map[string]time.Time, error)
	// RecordDedup persists one fresh idempotency entry (write-through).
	RecordDedup(key string, first time.Time) error
	// PruneDedup drops entries older than window relative to now and rewrites
	// the file, returning the number removed.
	PruneDedup(window time.Duration, now time.Time) (int, error)

	// AppendAudit appends one audit line to audit.jsonl and flushes it.
	AppendAudit(connID, code, detail string, at time.Time) error
	// ReadAudit reads every persisted audit line back, in append order.
	ReadAudit() ([]AuditRecord, error)

	// Close releases any held resources.
	Close() error
}

// compile-time assertion that FileStore implements Store.
var _ Store = (*FileStore)(nil)
