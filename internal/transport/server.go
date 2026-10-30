package transport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/auth"
	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
)

// Server hosts the real transport (TR-1..TR-8) over an existing relay. It
// authenticates agents (reusing internal/auth), registers each connection's
// transport with the relay exactly once so delivery semantics are reused, and
// serves both the WebSocket endpoint and the long-poll fallback.
type Server struct {
	relay  *relay.Relay
	store  *connection.Store
	signer *auth.Signer
	audit  delivery.AuditSink
	cfg    EndpointConfig
	clock  func() time.Time

	mu        sync.Mutex
	endpoints map[string]*Endpoint     // WebSocket endpoints by connection ID
	polls     map[string]*PollEndpoint // long-poll endpoints by connection ID
}

// Config tunes a Server; zero fields take package defaults.
type Config struct {
	SignatureSkew time.Duration // allowed handshake timestamp skew (default 30s, NFR-3)
	Endpoint      EndpointConfig
}

// NewServer builds a transport Server over the given relay and connection store.
// audit may be the relay's audit log so transport events share the trail.
func NewServer(r *relay.Relay, store *connection.Store, audit delivery.AuditSink, cfg Config) *Server {
	skew := cfg.SignatureSkew
	if skew == 0 {
		skew = 30 * time.Second
	}
	return &Server{
		relay:     r,
		store:     store,
		signer:    auth.NewSigner(skew),
		audit:     audit,
		cfg:       cfg.Endpoint,
		clock:     time.Now,
		endpoints: map[string]*Endpoint{},
		polls:     map[string]*PollEndpoint{},
	}
}

// SetClock overrides the server's time source and propagates it to new
// endpoints (tests).
func (s *Server) SetClock(f func() time.Time) { s.mu.Lock(); s.clock = f; s.mu.Unlock() }

// Routes registers the transport endpoints on a mux: the WebSocket endpoint and
// the long-poll GET/POST fallback.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/agent/ws", s.HandleWS)
	mux.HandleFunc("/agent/longpoll", s.HandleLongPoll)
}

// --- authentication (TR-2) --------------------------------------------------

// authResult is the outcome of handshake authentication.
type authResult struct {
	conn   *connection.Connection
	resume int64
}

// authenticate validates the handshake credentials carried in request headers
// or query parameters (TR-2), reusing the HMAC scheme from internal/auth. The
// signed body is the connection key bytes (documented in README/TRANSPORT-REPORT).
// It returns a stable §8 reason code on failure.
func (s *Server) authenticate(r *http.Request) (authResult, reason.Code, bool) {
	get := func(header, query string) string {
		if v := r.Header.Get(header); v != "" {
			return v
		}
		return r.URL.Query().Get(query)
	}
	key := get("X-Connection-Key", "connection_key")
	if connection.IsPlaceholderKey(key) {
		s.auditLog("", reason.MissingKey, "transport handshake: missing/placeholder key")
		return authResult{}, reason.MissingKey, false
	}
	conn, ok := s.store.Lookup(key)
	if !ok {
		s.auditLog("", reason.UnknownKey, "transport handshake: unknown key")
		return authResult{}, reason.UnknownKey, false
	}
	if conn.Expired(s.now()) {
		s.auditLog(conn.ID, reason.ExpiredKey, "transport handshake: expired key")
		return authResult{}, reason.ExpiredKey, false
	}
	if conn.RequireSignature {
		ts, _ := strconv.ParseInt(get("X-Timestamp", "ts"), 10, 64)
		m := auth.SignatureMaterial{
			Signature: get("X-Signature", "sig"),
			Timestamp: ts,
			Nonce:     get("X-Nonce", "nonce"),
			Body:      []byte(key),
		}
		if !s.signer.Verify(conn.Secret, m) {
			s.auditLog(conn.ID, reason.BadSignature, "transport handshake: bad signature")
			return authResult{}, reason.BadSignature, false
		}
	}
	resume, _ := strconv.ParseInt(get("X-Resume-Token", "resume_token"), 10, 64)
	return authResult{conn: conn, resume: resume}, "", true
}

// --- WebSocket endpoint (TR-1..TR-6) ---------------------------------------

// HandleWS authenticates, performs the RFC 6455 upgrade, establishes/ resumes a
// session, and runs the read loop until the socket closes.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	ar, code, ok := s.authenticate(r)
	if !ok {
		writeAuthError(w, code)
		return
	}
	ws, err := upgrade(w, r)
	if err != nil {
		// Pre-upgrade failure: a normal HTTP error is still possible.
		http.Error(w, "upgrade failed", http.StatusBadRequest)
		return
	}
	ep, ch, cerr := s.ensureWSEndpoint(ar.conn.ID)
	if cerr != nil {
		_ = ws.WriteJSON(Envelope{Type: MsgError, Error: &WireError{Code: reason.ConnectionLimit, Message: reason.ConnectionLimit.Message()}})
		ws.close()
		return
	}
	sessionID := newSessionID()
	token, pending := ep.attach(ws, sessionID, ar.resume)
	_ = ws.WriteJSON(Envelope{Type: MsgWelcome, SessionID: sessionID, ResumeToken: token})
	ep.replay(ws, pending) // redeliver unacked orders after the session is established (TR-4)

	// Flush orders the delivery.Channel buffered while the agent was fully
	// offline (FR-9); redelivery of sent-but-unacked orders was already issued
	// by attach (TR-4). Run it off the read loop since Deliver blocks on acks.
	if ch != nil {
		go ch.Reconnect(token)
	}

	s.readLoop(ep, ws)
}

// readLoop consumes agent→relay frames until the socket closes, routing ack /
// result / heartbeat and answering heartbeats with pong (TR-3, TR-5).
func (s *Server) readLoop(ep *Endpoint, ws *wsConn) {
	defer func() {
		ep.detach(ws)
		ws.close()
	}()
	for {
		op, payload, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if op == opPong {
			ep.handleBeat()
			continue
		}
		var env Envelope
		if json.Unmarshal(payload, &env) != nil {
			continue
		}
		switch env.Type {
		case MsgHeartbeat:
			ep.handleBeat()
			_ = ws.WriteJSON(Envelope{Type: MsgPong})
		case MsgAck:
			ep.handleBeat()
			ep.handleAck(env.DeliverySeq)
		case MsgResult:
			ep.handleBeat()
			if env.Result != nil {
				ep.handleResult(env.DeliverySeq, env.Result.accountResult())
			}
		case MsgHello:
			ep.handleBeat()
		}
	}
}

// ensureWSEndpoint returns the connection's WebSocket endpoint, creating and
// registering it with the relay (enforcing the concurrency limit, FR-17) on
// first use and reusing it on reconnect.
func (s *Server) ensureWSEndpoint(connID string) (*Endpoint, *delivery.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ep, ok := s.endpoints[connID]; ok {
		ch, _ := s.relay.Channel(connID)
		return ep, ch, nil
	}
	ep := NewEndpoint(connID, s.cfg, s.audit)
	ep.SetClock(s.clock)
	ch, err := s.relay.RegisterAgent(connID, ep)
	if err != nil {
		return nil, nil, err
	}
	s.endpoints[connID] = ep
	return ep, ch, nil
}

// Endpoint exposes a connection's WebSocket endpoint (tests/observability).
func (s *Server) Endpoint(connID string) (*Endpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.endpoints[connID]
	return ep, ok
}

// --- helpers ----------------------------------------------------------------

func (s *Server) now() time.Time {
	s.mu.Lock()
	f := s.clock
	s.mu.Unlock()
	return f()
}

func (s *Server) auditLog(connID string, code reason.Code, detail string) {
	if s.audit != nil {
		s.audit.Audit(connID, code, detail)
	}
}

// writeAuthError maps a handshake reason code to an HTTP status and a small
// JSON error body (TR-2). Codes mirror cmd/relay's webhook mapping.
func writeAuthError(w http.ResponseWriter, code reason.Code) {
	status := http.StatusUnauthorized
	switch code {
	case reason.ConnectionLimit:
		status = http.StatusTooManyRequests
	case reason.MalformedPayload:
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Envelope{Type: MsgError, Error: &WireError{Code: code, Message: code.Message()}})
}

// newSessionID returns a random session identifier (TR-2).
func newSessionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
