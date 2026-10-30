package transport

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// PollEndpoint is the HTTP long-poll fallback transport (TR-7) for agents that
// cannot hold a persistent socket. It implements delivery.Transport, carrying
// the identical typed envelope and the same at-least-once + idempotency
// guarantees as the WebSocket path: orders are queued with a monotonic
// delivery_seq, a GET drains the next unacked order(s), and a POST acks or
// reports results. An unacked order is re-returned on each poll (at-least-once;
// the agent dedups by idempotency key) until it is acked or ages out of the
// replay window, at which point it is dropped with delivery_expired.
type PollEndpoint struct {
	connID string
	window time.Duration
	audit  delivery.AuditSink
	clock  func() time.Time

	mu       sync.Mutex
	nextSeq  int64
	ackedSeq int64
	queue    map[int64]*pollItem
}

type pollItem struct {
	seq       int64
	accountID string
	order     delivery.ResolvedOrder
	at        time.Time
}

// NewPollEndpoint builds a long-poll endpoint for one connection.
func NewPollEndpoint(connID string, window time.Duration, audit delivery.AuditSink) *PollEndpoint {
	if window == 0 {
		window = DefaultReplayWindow
	}
	return &PollEndpoint{
		connID: connID,
		window: window,
		audit:  audit,
		clock:  time.Now,
		queue:  map[int64]*pollItem{},
	}
}

// SetClock overrides the time source (tests).
func (p *PollEndpoint) SetClock(f func() time.Time) { p.mu.Lock(); p.clock = f; p.mu.Unlock() }

// Online implements delivery.Transport. The long-poll transport always accepts
// orders into its queue (the agent collects them on its own schedule), so the
// delivery.Channel delivers rather than buffering; at-least-once and the replay
// window are enforced here on drain.
func (p *PollEndpoint) Online() bool { return true }

// Deliver implements delivery.Transport: it queues the resolved order with a
// fresh delivery_seq and returns a Buffered (queued) result. The execution
// outcome is reported asynchronously by the agent via POST.
func (p *PollEndpoint) Deliver(accountID string, order delivery.ResolvedOrder) delivery.AccountResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purgeLocked(p.clock())
	p.nextSeq++
	p.queue[p.nextSeq] = &pollItem{seq: p.nextSeq, accountID: accountID, order: order, at: p.clock()}
	return delivery.AccountResult{AccountID: accountID, Status: delivery.StatusBuffered}
}

// QueueLen reports the number of unacked queued orders (observability/tests).
func (p *PollEndpoint) QueueLen() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.queue) }

// ResumeToken returns the highest acked delivery_seq (tests).
func (p *PollEndpoint) ResumeToken() int64 { p.mu.Lock(); defer p.mu.Unlock(); return p.ackedSeq }

// drain returns the unacked orders within the replay window as order envelopes,
// in ascending seq order, and drops any that have aged out (TR-6/EC-9). If
// resume > 0, orders at or below it are treated as acked first.
func (p *PollEndpoint) drain(resume int64) []Envelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	if resume > p.ackedSeq {
		p.ackedSeq = resume
	}
	now := p.clock()
	p.purgeLocked(now)
	seqs := make([]int64, 0, len(p.queue))
	for s := range p.queue {
		if s > p.ackedSeq {
			seqs = append(seqs, s)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	out := make([]Envelope, 0, len(seqs))
	for _, s := range seqs {
		it := p.queue[s]
		out = append(out, orderEnvelope(it.seq, it.accountID, it.order))
	}
	return out
}

// purgeLocked drops queued orders older than the replay window, logging
// delivery_expired for each (caller holds p.mu).
func (p *PollEndpoint) purgeLocked(now time.Time) {
	for s, it := range p.queue {
		if now.Sub(it.at) > p.window {
			delete(p.queue, s)
			if p.audit != nil {
				p.audit.Audit(p.connID, reason.DeliveryExpired, "long-poll order aged out of replay window")
			}
		}
	}
}

// ackResult records an ack or result by delivery_seq, removing the queued order
// and advancing the resume token (TR-3/TR-4).
func (p *PollEndpoint) ackResult(seq int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.queue, seq)
	if seq > p.ackedSeq {
		p.ackedSeq = seq
	}
}

// --- HTTP handlers (TR-7) ---------------------------------------------------

// HandleLongPoll dispatches the long-poll GET (receive next order(s)) and POST
// (ack / report result) on a single path.
func (s *Server) HandleLongPoll(w http.ResponseWriter, r *http.Request) {
	ar, code, ok := s.authenticate(r)
	if !ok {
		writeAuthError(w, code)
		return
	}
	ep, cerr := s.ensurePollEndpoint(ar.conn.ID)
	if cerr != nil {
		writeAuthError(w, reason.ConnectionLimit)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.longPollGet(w, ep, ar.resume)
	case http.MethodPost:
		s.longPollPost(w, ep, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// longPollGet returns the next unacked order(s) as a JSON array of envelopes,
// or 204 when the queue is empty (TR-7).
func (s *Server) longPollGet(w http.ResponseWriter, ep *PollEndpoint, resume int64) {
	orders := ep.drain(resume)
	if len(orders) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(orders)
}

// longPollPost applies one or more ack/result envelopes posted by the agent
// (TR-7). It accepts either a single envelope object or a JSON array.
func (s *Server) longPollPost(w http.ResponseWriter, ep *PollEndpoint, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeAuthError(w, reason.MalformedPayload)
		return
	}
	var envs []Envelope
	if err := json.Unmarshal(raw, &envs); err != nil {
		var single Envelope
		if err := json.Unmarshal(raw, &single); err != nil {
			writeAuthError(w, reason.MalformedPayload)
			return
		}
		envs = []Envelope{single}
	}
	for _, env := range envs {
		switch env.Type {
		case MsgAck:
			ep.ackResult(env.DeliverySeq)
		case MsgResult:
			ep.ackResult(env.DeliverySeq)
			if env.Result != nil && env.Result.Status == delivery.StatusRejected {
				s.auditLog(ep.connID, reason.BrokerRejected, "long-poll result: "+env.Result.BrokerCode)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// ensurePollEndpoint returns the connection's long-poll endpoint, creating and
// registering it with the relay (enforcing the concurrency limit, FR-17) on
// first use.
func (s *Server) ensurePollEndpoint(connID string) (*PollEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ep, ok := s.polls[connID]; ok {
		return ep, nil
	}
	ep := NewPollEndpoint(connID, s.cfg.Window, s.audit)
	ep.SetClock(s.clock)
	if _, err := s.relay.RegisterAgent(connID, ep); err != nil {
		return nil, err
	}
	s.polls[connID] = ep
	return ep, nil
}

// PollEndpoint exposes a connection's long-poll endpoint (tests/observability).
func (s *Server) PollEndpoint(connID string) (*PollEndpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.polls[connID]
	return ep, ok
}
