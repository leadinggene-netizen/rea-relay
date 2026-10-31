package relay

import (
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// AuditEvent is an immutable record of a rejection, skip, or notable delivery
// event (spec §4 AuditEvent, §8: rejections are logged even when not surfaced).
type AuditEvent struct {
	ConnectionID string
	Code         reason.Code
	Detail       string
	At           time.Time
}

// AuditPersister is an optional durable backing for an AuditLog: each appended
// event is also written through to it, producing a durable append-only record
// (§8). It is satisfied by internal/store's file-backed Store (AppendAudit).
// Defined here to keep the dependency direction one-way (store imports relay's
// types, not vice versa).
type AuditPersister interface {
	AppendAudit(connID, code, detail string, at time.Time) error
}

// AuditLog is an append-only audit trail kept in memory and, when a persister
// is attached, mirrored to durable storage. It satisfies delivery.AuditSink and
// is safe for concurrent use. (NFR-3 immutable audit trail — modeled
// append-only here.)
type AuditLog struct {
	mu      sync.Mutex
	events  []AuditEvent
	clock   func() time.Time
	persist AuditPersister
}

// NewAuditLog returns an empty audit log.
func NewAuditLog() *AuditLog { return &AuditLog{clock: time.Now} }

// SetPersister attaches (or clears, with nil) a durable backing. Events
// appended after this call are also written through to the persister.
func (l *AuditLog) SetPersister(p AuditPersister) {
	l.mu.Lock()
	l.persist = p
	l.mu.Unlock()
}

// Audit appends an event (implements delivery.AuditSink).
func (l *AuditLog) Audit(connID string, code reason.Code, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.clock()
	l.events = append(l.events, AuditEvent{ConnectionID: connID, Code: code, Detail: detail, At: at})
	if l.persist != nil {
		// Best-effort write-through; in-memory trail remains authoritative.
		_ = l.persist.AppendAudit(connID, string(code), detail, at)
	}
}

// Events returns a snapshot copy of all recorded events.
func (l *AuditLog) Events() []AuditEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]AuditEvent(nil), l.events...)
}

// Count returns how many events carry the given reason code (test helper).
func (l *AuditLog) Count(code reason.Code) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.Code == code {
			n++
		}
	}
	return n
}
