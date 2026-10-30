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

// AuditLog is an in-memory, append-only audit trail. It satisfies
// delivery.AuditSink and is safe for concurrent use. (NFR-3 immutable audit
// trail — modeled append-only here.)
type AuditLog struct {
	mu     sync.Mutex
	events []AuditEvent
	clock  func() time.Time
}

// NewAuditLog returns an empty audit log.
func NewAuditLog() *AuditLog { return &AuditLog{clock: time.Now} }

// Audit appends an event (implements delivery.AuditSink).
func (l *AuditLog) Audit(connID string, code reason.Code, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, AuditEvent{ConnectionID: connID, Code: code, Detail: detail, At: l.clock()})
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
