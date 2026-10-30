package transport

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/auth"
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
)

// Unit check: the RFC 6455 Sec-WebSocket-Accept vector from the spec text.
func TestAcceptKeyVector(t *testing.T) {
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key wrong: %q", got)
	}
}

// TR-2: handshake accepted with valid auth; welcome carries a session id.
func TestWSHandshakeAccepted(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, resp := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	defer c.close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("want 101, got %d", resp.StatusCode)
	}
	w := c.expectEnvelope(t, MsgWelcome)
	if w.SessionID == "" {
		t.Fatalf("welcome missing session_id")
	}
}

// TR-2 / TC-X2: handshake rejected with a bad HMAC on a signature-required
// connection, with the stable bad_signature reason; a correct signature is
// accepted.
func TestWSHandshakeSignature(t *testing.T) {
	h := newHarness(t, harnessOpts{requireSig: true, secret: "s3cr3t"})

	// Bad signature -> 401 bad_signature, no upgrade.
	bad, resp := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey+"&ts=1&nonce=n1&sig=deadbeef", nil)
	defer bad.close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad sig, got %d", resp.StatusCode)
	}
	if h.audit.Count(reason.BadSignature) == 0 {
		t.Fatalf("bad_signature must be audited")
	}

	// Correct signature over the connection key -> upgraded + welcome.
	ts := time.Now().Unix()
	sig := auth.Compute("s3cr3t", []byte(tKey), ts, "n2")
	q := "/agent/ws?connection_key=" + tKey + "&ts=" + strconv.FormatInt(ts, 10) + "&nonce=n2&sig=" + sig
	good, resp2 := dialWS(t, h.srv.URL, q, nil)
	defer good.close()
	if resp2.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("want 101 for good sig, got %d", resp2.StatusCode)
	}
	good.expectEnvelope(t, MsgWelcome)
}

// TR-3 / TR-4: an order is delivered over the socket with a monotonic
// delivery_seq and the fully-resolved fields, then acked + resulted; the
// webhook dispatch observes the fill.
func TestWSOrderDeliveredThenAcked(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, _ := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	defer c.close()
	c.expectEnvelope(t, MsgWelcome)

	resCh := make(chan relay.Result, 1)
	go func() { resCh <- h.relay.Process(relay.Request{Body: openLongBody("ws-1")}) }()

	order := c.expectEnvelope(t, MsgOrder)
	if order.DeliverySeq != 1 {
		t.Fatalf("want delivery_seq 1, got %d", order.DeliverySeq)
	}
	if order.Order == nil || order.Order.AccountID != "acct-1" || order.Order.Lots != 0.10 {
		t.Fatalf("unexpected order payload: %+v", order.Order)
	}
	if order.Order.SLPrice != nil {
		t.Fatalf("fixed_lots order should carry no stop here")
	}

	// Ack then report a fill.
	if err := c.writeJSON(Envelope{Type: MsgAck, DeliverySeq: order.DeliverySeq}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := c.writeJSON(Envelope{Type: MsgResult, DeliverySeq: order.DeliverySeq,
		Result: &WireResult{AccountID: "acct-1", Status: delivery.StatusFilled, FinalVolume: 0.10}}); err != nil {
		t.Fatalf("result: %v", err)
	}

	res := <-resCh
	if res.Overall != delivery.StatusFilled {
		t.Fatalf("want filled, got overall=%s code=%s", res.Overall, res.Code)
	}
	if ep, _ := h.ts.Endpoint("conn-1"); ep.ResumeToken() != 1 {
		t.Fatalf("resume token should advance to 1")
	}
}

// TR-4 / TR-6: an order sent but never acked is redelivered exactly once on
// reconnect within the window; beyond the window it is dropped with
// delivery_expired and not redelivered.
func TestWSUnackedRedeliveredOnReconnect(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c1, _ := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	c1.expectEnvelope(t, MsgWelcome)

	resCh := make(chan relay.Result, 1)
	go func() { resCh <- h.relay.Process(relay.Request{Body: openLongBody("ws-redeliver")}) }()

	first := c1.expectEnvelope(t, MsgOrder)
	if first.DeliverySeq != 1 {
		t.Fatalf("want seq 1, got %d", first.DeliverySeq)
	}
	// Drop the socket WITHOUT acking: Deliver releases as buffered, order stays pending.
	c1.close()
	if res := <-resCh; res.Overall != delivery.StatusBuffered {
		t.Fatalf("want buffered after drop, got %s", res.Overall)
	}

	// Reconnect within the window (clock not advanced): the unacked order replays once.
	c2, _ := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey+"&resume_token=0", nil)
	defer c2.close()
	c2.expectEnvelope(t, MsgWelcome)
	redelivered := c2.expectEnvelope(t, MsgOrder)
	if redelivered.DeliverySeq != 1 {
		t.Fatalf("want redelivered seq 1, got %d", redelivered.DeliverySeq)
	}
	// No second redelivery.
	if env, err := c2.readEnvelope(t, 300*time.Millisecond); err == nil {
		t.Fatalf("expected no further frame, got %+v", env)
	}
	// Ack it so it clears.
	_ = c2.writeJSON(Envelope{Type: MsgResult, DeliverySeq: 1,
		Result: &WireResult{AccountID: "acct-1", Status: delivery.StatusFilled, FinalVolume: 0.10}})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ep, _ := h.ts.Endpoint("conn-1"); ep.PendingLen() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ep, _ := h.ts.Endpoint("conn-1"); ep.PendingLen() != 0 {
		t.Fatalf("pending should be cleared after result")
	}

	// --- Beyond-window variant on a fresh connection ---
	h2 := newHarness(t, harnessOpts{})
	d1, _ := dialWS(t, h2.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	d1.expectEnvelope(t, MsgWelcome)
	rc := make(chan relay.Result, 1)
	go func() { rc <- h2.relay.Process(relay.Request{Body: openLongBody("ws-expire")}) }()
	d1.expectEnvelope(t, MsgOrder)
	d1.close()
	<-rc
	// Advance past the 60s replay window, then reconnect.
	h2.clk.advance(90 * time.Second)
	d2, _ := dialWS(t, h2.srv.URL, "/agent/ws?connection_key="+tKey+"&resume_token=0", nil)
	defer d2.close()
	d2.expectEnvelope(t, MsgWelcome)
	if env, err := d2.readEnvelope(t, 300*time.Millisecond); err == nil {
		t.Fatalf("expected no redelivery beyond window, got %+v", env)
	}
	if h2.audit.Count(reason.DeliveryExpired) == 0 {
		t.Fatalf("want delivery_expired audited beyond window")
	}
}

// TR-5: a missed-heartbeat threshold marks the account offline (Online() flips
// false and the transition is audited).
func TestWSHeartbeatOffline(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, _ := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	defer c.close()
	c.expectEnvelope(t, MsgWelcome)

	ep, ok := h.ts.Endpoint("conn-1")
	if !ok {
		t.Fatalf("endpoint missing")
	}
	if !ep.Online() {
		t.Fatalf("endpoint should be online right after connect")
	}
	// Advance beyond missedThreshold (3) * interval (15s) with no heartbeats.
	h.clk.advance(3*DefaultHeartbeatInterval + time.Second)
	if ep.Online() {
		t.Fatalf("endpoint should be offline after missed heartbeats")
	}
	if h.audit.Count(reason.DeliveryExpired) == 0 {
		t.Fatalf("offline transition should be audited")
	}
}

// TR-5: an explicit heartbeat frame is answered with a pong and refreshes
// liveness.
func TestWSHeartbeatPong(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c, _ := dialWS(t, h.srv.URL, "/agent/ws?connection_key="+tKey, nil)
	defer c.close()
	c.expectEnvelope(t, MsgWelcome)
	if err := c.writeJSON(Envelope{Type: MsgHeartbeat}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	c.expectEnvelope(t, MsgPong)
}
