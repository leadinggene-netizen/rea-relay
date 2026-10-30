// Package delivery carries resolved orders to the terminal agent with
// at-least-once semantics, per-connection ordering, fan-out to multiple
// accounts, pause/resume, and an offline buffer with resume-token replay
// (FR-8..11). The terminal agent itself (FR-12..16) is out of scope here and is
// modeled behind the Transport interface; MockAgent is the in-memory stand-in
// used by tests to exercise fills/rejects/rounding/no-op and offline/replay.
package delivery

import (
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// Status is the outcome of a single account's execution attempt (FR-16).
type Status string

const (
	StatusFilled    Status = "filled"
	StatusPartial   Status = "partial"
	StatusRejected  Status = "rejected"
	StatusSkipped   Status = "skipped"
	StatusNoOp      Status = "no_op"
	StatusBuffered  Status = "buffered"
	StatusExpired   Status = "expired"
	StatusDuplicate Status = "duplicate"
)

// ResolvedOrder is a fully-resolved order (FR-7): absolute volume and SL/TP, no
// risk math left for the agent.
type ResolvedOrder struct {
	InstructionID   string
	IdempotencyKey  string
	ConnectionID    string
	Action          instruction.Action
	BrokerSymbol    string
	Lots            float64
	SLPrice         *float64
	TPPrice         *float64
	PendingPrice    *float64
	PartialFraction *instruction.PartialFraction
	CloseVolume     *float64
	Comment         string

	// Seq is assigned by the dispatcher and preserves per-connection ordering
	// and backs resume-token replay (FR-8, FR-9).
	Seq int64
}

// AccountResult is the per-account outcome of a delivery (FR-10, FR-16).
type AccountResult struct {
	AccountID   string
	Status      Status
	FinalVolume float64
	BrokerCode  string      // broker result code when rejected/filled
	Reason      reason.Code // set for skips/rejections/rounding
	Deduped     bool        // the agent recognized a repeat idempotency key
}

// Transport is the boundary to a terminal agent (FR-8). Online reports whether
// the agent currently holds its channel; Deliver executes one resolved order on
// one account and returns the agent's result. Implementations dedup by
// idempotency key (agent-side) to make at-least-once effectively exactly-once.
type Transport interface {
	Online() bool
	Deliver(accountID string, order ResolvedOrder) AccountResult
}

// bufferedItem is an order held while the agent is offline (FR-9).
type bufferedItem struct {
	order    ResolvedOrder
	accounts []string
	at       time.Time
}

// Channel is the per-connection delivery state machine: it serializes dispatch
// (ordering, FR-8), enforces pause (FR-11), and buffers/replays across offline
// windows (FR-9). It is safe for concurrent use.
type Channel struct {
	ConnectionID string
	Transport    Transport
	Window       time.Duration // offline buffer/replay window (FR-9)

	mu       sync.Mutex
	paused   bool
	seq      int64
	lastAck  int64 // highest Seq the agent has acknowledged (resume token)
	buffer   []bufferedItem
	clock    func() time.Time
	auditLog AuditSink
}

// AuditSink records delivery-related audit events (spec §4 AuditEvent, §8).
type AuditSink interface {
	Audit(connID string, code reason.Code, detail string)
}

// NewChannel builds a per-connection delivery channel.
func NewChannel(connID string, t Transport, window time.Duration, audit AuditSink) *Channel {
	return &Channel{
		ConnectionID: connID,
		Transport:    t,
		Window:       window,
		clock:        time.Now,
		auditLog:     audit,
	}
}

// SetClock overrides the time source (tests).
func (c *Channel) SetClock(f func() time.Time) { c.mu.Lock(); c.clock = f; c.mu.Unlock() }

// Pause halts new entries for this connection (FR-11). Existing positions are
// untouched; non-entry actions still flow.
func (c *Channel) Pause() { c.mu.Lock(); c.paused = true; c.mu.Unlock() }

// Resume lifts a pause (FR-11).
func (c *Channel) Resume() { c.mu.Lock(); c.paused = false; c.mu.Unlock() }

// Paused reports the current pause state.
func (c *Channel) Paused() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.paused }

// DispatchOutcome is the connection-level result of a dispatch.
type DispatchOutcome struct {
	Overall Status          // filled / partial / rejected / skipped / no_op / buffered
	Results []AccountResult // per-account (FR-10); empty when buffered
	Seq     int64
}

// Dispatch delivers order to all target accounts, fanning out and tracking each
// result independently (FR-10). isEntry governs the pause gate (FR-11). When the
// agent is offline the order is buffered for later replay (FR-9) and the outcome
// is Buffered. The channel mutex guarantees per-connection ordering (FR-8).
func (c *Channel) Dispatch(order ResolvedOrder, accounts []string, isEntry bool) DispatchOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.paused && isEntry {
		c.auditLog.Audit(c.ConnectionID, reason.AgentPaused, "entry skipped: connection paused")
		results := make([]AccountResult, 0, len(accounts))
		for _, a := range accounts {
			results = append(results, AccountResult{AccountID: a, Status: StatusSkipped, Reason: reason.AgentPaused})
		}
		return DispatchOutcome{Overall: StatusSkipped, Results: results}
	}

	c.seq++
	order.Seq = c.seq

	if !c.Transport.Online() {
		c.buffer = append(c.buffer, bufferedItem{order: order, accounts: append([]string(nil), accounts...), at: c.clock()})
		c.auditLog.Audit(c.ConnectionID, reason.DeliveryExpired, "agent offline: buffered for replay")
		return DispatchOutcome{Overall: StatusBuffered, Seq: order.Seq}
	}

	results := c.deliverAll(order, accounts)
	c.lastAck = order.Seq
	return DispatchOutcome{Overall: overallStatus(results), Results: results, Seq: order.Seq}
}

// deliverAll fans the order out to every account (caller holds the lock).
func (c *Channel) deliverAll(order ResolvedOrder, accounts []string) []AccountResult {
	results := make([]AccountResult, 0, len(accounts))
	for _, a := range accounts {
		results = append(results, c.Transport.Deliver(a, order))
	}
	return results
}

// ReplayResult reports what a reconnect did (FR-9).
type ReplayResult struct {
	Replayed []DispatchOutcome // delivered on reconnect (Seq > resume token, within window)
	Expired  []ResolvedOrder   // dropped: aged out of the window (delivery_expired)
}

// Reconnect replays buffered orders after the agent returns. The agent presents
// resumeToken = the highest Seq it already acknowledged; only orders with
// Seq > resumeToken are re-sent (FR-9). Orders older than the window are dropped
// and logged delivery_expired (EC-9). Ordering is preserved.
func (c *Channel) Reconnect(resumeToken int64) ReplayResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if resumeToken > c.lastAck {
		c.lastAck = resumeToken
	}
	now := c.clock()
	var rr ReplayResult
	remaining := c.buffer[:0]
	// Work on a snapshot to keep ordering deterministic.
	items := append([]bufferedItem(nil), c.buffer...)
	c.buffer = remaining
	for _, it := range items {
		if it.order.Seq <= resumeToken {
			continue // already acknowledged by the agent
		}
		if now.Sub(it.at) > c.Window {
			c.auditLog.Audit(c.ConnectionID, reason.DeliveryExpired, "buffered delivery aged out of replay window")
			rr.Expired = append(rr.Expired, it.order)
			continue
		}
		if !c.Transport.Online() {
			// Still offline: keep it buffered.
			c.buffer = append(c.buffer, it)
			continue
		}
		results := c.deliverAll(it.order, it.accounts)
		c.lastAck = it.order.Seq
		rr.Replayed = append(rr.Replayed, DispatchOutcome{Overall: overallStatus(results), Results: results, Seq: it.order.Seq})
	}
	return rr
}

// BufferLen reports how many orders are currently buffered (observability/tests).
func (c *Channel) BufferLen() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.buffer) }

// Reduce reduces per-account results to a connection-level status (exported for
// the relay orchestrator). See overallStatus.
func Reduce(results []AccountResult) Status { return overallStatus(results) }

// overallStatus reduces per-account results to a connection-level status
// (EC-12, TC-X6): all filled -> filled; mix of success and failure -> partial;
// all failed -> rejected; otherwise reflect the common non-fill status.
func overallStatus(results []AccountResult) Status {
	if len(results) == 0 {
		return StatusNoOp
	}
	var ok, bad, noop, skipped, buffered int
	for _, r := range results {
		switch r.Status {
		case StatusFilled, StatusPartial, StatusDuplicate:
			ok++
		case StatusRejected:
			bad++
		case StatusNoOp:
			noop++
		case StatusSkipped:
			skipped++
		case StatusBuffered:
			buffered++
		}
	}
	switch {
	case buffered == len(results):
		return StatusBuffered
	case ok > 0 && bad > 0:
		return StatusPartial
	case ok == len(results):
		return StatusFilled
	case bad == len(results):
		return StatusRejected
	case noop == len(results):
		return StatusNoOp
	case skipped == len(results):
		return StatusSkipped
	case ok > 0:
		return StatusPartial
	default:
		return StatusRejected
	}
}
