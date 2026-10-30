package transport

import (
	"sort"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// Defaults for the WebSocket transport (spec §5, §13, D-5).
const (
	DefaultHeartbeatInterval = 15 * time.Second // TR-5 heartbeat interval.
	DefaultMissedThreshold   = 3                // TR-5 missed heartbeats before offline.
	DefaultReplayWindow      = 60 * time.Second // §5/D-5/TR-4 unacked replay window.
	DefaultDeliverTimeout    = 5 * time.Second  // how long Deliver waits for a result.
)

// pendingDelivery tracks one order sent on the wire that has not yet produced a
// result. It backs at-least-once redelivery on reconnect (TR-4) and feeds the
// blocking Deliver its result when the agent reports one.
type pendingDelivery struct {
	seq       int64
	accountID string
	order     delivery.ResolvedOrder
	at        time.Time // enqueue time, for the replay-window check (TR-6)
	resultCh  chan delivery.AccountResult
	acked     bool
}

// Endpoint is the per-connection, socket-independent WebSocket transport. It
// implements delivery.Transport so the delivery.Channel drives it for fan-out,
// pause/resume and ordering (TR-8); the Endpoint itself owns the wire-level
// concerns: monotonic delivery_seq, the typed envelope, at-least-once with
// agent acks, redelivery of unacked orders on reconnect within the window, and
// the heartbeat/offline state machine (TR-2..TR-6).
//
// It outlives any single socket: a reconnecting agent re-attaches via Resume,
// so orders sent-but-unacked when a socket dropped survive to be redelivered.
type Endpoint struct {
	connID            string
	window            time.Duration
	deliverTimeout    time.Duration
	heartbeatInterval time.Duration
	missedThreshold   int
	audit             delivery.AuditSink
	clock             func() time.Time

	mu         sync.Mutex
	conn       *wsConn
	sessionID  string
	nextSeq    int64
	ackedSeq   int64 // resume token = highest delivery_seq the agent has completed
	pending    map[int64]*pendingDelivery
	lastBeat   time.Time
	socketUp   bool
	offlineLog bool // whether the current offline state was already audited
}

// EndpointConfig tunes an Endpoint; zero fields take the package defaults.
type EndpointConfig struct {
	Window            time.Duration
	DeliverTimeout    time.Duration
	HeartbeatInterval time.Duration
	MissedThreshold   int
}

func (c EndpointConfig) withDefaults() EndpointConfig {
	if c.Window == 0 {
		c.Window = DefaultReplayWindow
	}
	if c.DeliverTimeout == 0 {
		c.DeliverTimeout = DefaultDeliverTimeout
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.MissedThreshold == 0 {
		c.MissedThreshold = DefaultMissedThreshold
	}
	return c
}

// NewEndpoint builds a WebSocket endpoint for one connection.
func NewEndpoint(connID string, cfg EndpointConfig, audit delivery.AuditSink) *Endpoint {
	cfg = cfg.withDefaults()
	return &Endpoint{
		connID:            connID,
		window:            cfg.Window,
		deliverTimeout:    cfg.DeliverTimeout,
		heartbeatInterval: cfg.HeartbeatInterval,
		missedThreshold:   cfg.MissedThreshold,
		audit:             audit,
		clock:             time.Now,
		pending:           map[int64]*pendingDelivery{},
	}
}

// SetClock overrides the time source (tests).
func (e *Endpoint) SetClock(f func() time.Time) { e.mu.Lock(); e.clock = f; e.mu.Unlock() }

// ResumeToken returns the current resume token (highest completed delivery_seq).
func (e *Endpoint) ResumeToken() int64 { e.mu.Lock(); defer e.mu.Unlock(); return e.ackedSeq }

// SessionID returns the current session id.
func (e *Endpoint) SessionID() string { e.mu.Lock(); defer e.mu.Unlock(); return e.sessionID }

// PendingLen reports how many orders are awaiting a result (observability/tests).
func (e *Endpoint) PendingLen() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.pending) }

// Online implements delivery.Transport (TR-5). It reports true only while a
// socket is attached and the heartbeat is fresh; a missed-heartbeat threshold
// flips the account offline (audited once) so subsequent dispatches buffer in
// the delivery.Channel until the agent returns.
func (e *Endpoint) Online() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.liveLocked(e.clock())
}

// liveLocked evaluates heartbeat liveness, flipping and auditing the offline
// transition the first time it is observed (caller holds e.mu).
func (e *Endpoint) liveLocked(now time.Time) bool {
	if !e.socketUp || e.conn == nil {
		return false
	}
	if now.Sub(e.lastBeat) > time.Duration(e.missedThreshold)*e.heartbeatInterval {
		if !e.offlineLog {
			e.offlineLog = true
			e.auditLog(reason.DeliveryExpired, "agent heartbeat missed: marked offline")
		}
		return false
	}
	return true
}

// Deliver implements delivery.Transport: it sends one resolved order on the
// live socket with a fresh monotonic delivery_seq, records it as unacked for
// at-least-once redelivery, and blocks (bounded by DeliverTimeout) for the
// agent's result. If the socket is down or drops mid-flight, or the agent does
// not answer in time, it returns a Buffered result and the order stays pending
// for redelivery on the next Resume (TR-4).
func (e *Endpoint) Deliver(accountID string, order delivery.ResolvedOrder) delivery.AccountResult {
	e.mu.Lock()
	if !e.liveLocked(e.clock()) {
		e.mu.Unlock()
		return delivery.AccountResult{AccountID: accountID, Status: delivery.StatusBuffered}
	}
	e.nextSeq++
	seq := e.nextSeq
	pd := &pendingDelivery{
		seq: seq, accountID: accountID, order: order, at: e.clock(),
		resultCh: make(chan delivery.AccountResult, 1),
	}
	e.pending[seq] = pd
	conn := e.conn
	env := orderEnvelope(seq, accountID, order)
	e.mu.Unlock()

	if err := conn.WriteJSON(env); err != nil {
		return delivery.AccountResult{AccountID: accountID, Status: delivery.StatusBuffered}
	}
	select {
	case res := <-pd.resultCh:
		return res
	case <-time.After(e.deliverTimeout):
		// No result in time: keep it pending for redelivery; report buffered.
		return delivery.AccountResult{AccountID: accountID, Status: delivery.StatusBuffered}
	}
}

// attach binds a freshly-authenticated socket to the endpoint and establishes a
// session, honoring the agent's presented resume token (TR-2). It returns the
// authoritative resume token and the list of still-valid unacked orders to
// redeliver (TR-4); orders older than the replay window are dropped with
// delivery_expired (TR-6). The caller sends the welcome envelope before
// replaying so the agent sees the session established first.
func (e *Endpoint) attach(conn *wsConn, sessionID string, agentResume int64) (int64, []*pendingDelivery) {
	e.mu.Lock()
	// Detach any prior socket.
	if e.conn != nil && e.conn != conn {
		e.conn.close()
	}
	e.conn = conn
	e.sessionID = sessionID
	e.socketUp = true
	e.offlineLog = false
	e.lastBeat = e.clock()
	if agentResume > e.ackedSeq {
		e.ackedSeq = agentResume
	}

	now := e.clock()
	seqs := make([]int64, 0, len(e.pending))
	for s := range e.pending {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	var replay []*pendingDelivery
	for _, s := range seqs {
		pd := e.pending[s]
		if pd.seq <= e.ackedSeq {
			delete(e.pending, s)
			continue
		}
		if now.Sub(pd.at) > e.window { // TR-6: aged out of the replay window.
			delete(e.pending, s)
			e.auditLog(reason.DeliveryExpired, "unacked order aged out of replay window")
			select {
			case pd.resultCh <- delivery.AccountResult{AccountID: pd.accountID, Status: delivery.StatusExpired, Reason: reason.DeliveryExpired}:
			default:
			}
			continue
		}
		replay = append(replay, pd)
	}
	token := e.ackedSeq
	e.mu.Unlock()
	return token, replay
}

// replay redelivers the given unacked orders on conn in ascending seq order,
// preserving per-connection ordering (TR-4). Called after the welcome envelope.
func (e *Endpoint) replay(conn *wsConn, items []*pendingDelivery) {
	for _, pd := range items {
		_ = conn.WriteJSON(orderEnvelope(pd.seq, pd.accountID, pd.order))
	}
}

// detach clears the live socket (called when its read loop ends). Pending
// orders are left intact for redelivery on the next attach (TR-4/TR-6), but any
// Deliver currently blocked on one is released promptly with a Buffered result
// so it does not wait out the full DeliverTimeout.
func (e *Endpoint) detach(conn *wsConn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != conn {
		return
	}
	e.conn = nil
	e.socketUp = false
	for _, pd := range e.pending {
		select {
		case pd.resultCh <- delivery.AccountResult{AccountID: pd.accountID, Status: delivery.StatusBuffered}:
		default:
		}
	}
}

// handleBeat refreshes the heartbeat clock (TR-5) and clears any offline flag.
func (e *Endpoint) handleBeat() {
	e.mu.Lock()
	e.lastBeat = e.clock()
	e.offlineLog = false
	e.mu.Unlock()
}

// handleAck records an agent ack by delivery_seq (TR-3). An ack marks receipt;
// completion (and resume-token advance) is driven by the result.
func (e *Endpoint) handleAck(seq int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pd, ok := e.pending[seq]; ok {
		pd.acked = true
	}
}

// handleResult records an execution outcome by delivery_seq (TR-3): it clears
// the pending entry, advances the resume token, and unblocks any Deliver still
// waiting on this delivery.
func (e *Endpoint) handleResult(seq int64, res delivery.AccountResult) {
	e.mu.Lock()
	pd, ok := e.pending[seq]
	if ok {
		delete(e.pending, seq)
		if seq > e.ackedSeq {
			e.ackedSeq = seq
		}
	}
	e.mu.Unlock()
	if ok {
		select {
		case pd.resultCh <- res:
		default:
		}
	}
}

// auditLog records a transport audit event (caller need not hold e.mu).
func (e *Endpoint) auditLog(code reason.Code, detail string) {
	if e.audit != nil {
		e.audit.Audit(e.connID, code, detail)
	}
}
