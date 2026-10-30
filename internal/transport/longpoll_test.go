package transport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
)

// longPollGet issues an authenticated GET and returns the decoded order
// envelopes (nil on 204).
func longPollGet(t *testing.T, base string, resume string) []Envelope {
	t.Helper()
	u := base + "/agent/longpoll?connection_key=" + tKey
	if resume != "" {
		u += "&resume_token=" + resume
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status %d", resp.StatusCode)
	}
	var envs []Envelope
	if err := json.NewDecoder(resp.Body).Decode(&envs); err != nil {
		t.Fatalf("decode orders: %v", err)
	}
	return envs
}

// longPollPost posts ack/result envelopes.
func longPollPost(t *testing.T, base string, envs []Envelope) int {
	t.Helper()
	b, _ := json.Marshal(envs)
	resp, err := http.Post(base+"/agent/longpoll?connection_key="+tKey, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TR-7: the long-poll fallback delivers an order (GET), the agent reports a
// result (POST), the order clears, and a subsequent GET is empty — the same
// envelope + at-least-once + idempotency guarantees as the WebSocket path.
func TestLongPollDeliverAndResult(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// Register the long-poll endpoint (first GET is empty).
	if got := longPollGet(t, h.srv.URL, ""); got != nil {
		t.Fatalf("expected empty initial poll, got %+v", got)
	}

	res := h.relay.Process(relay.Request{Body: openLongBody("lp-1")})
	if res.Overall != delivery.StatusBuffered {
		t.Fatalf("long-poll dispatch should queue (buffered), got %s", res.Overall)
	}

	orders := longPollGet(t, h.srv.URL, "")
	if len(orders) != 1 {
		t.Fatalf("want 1 order, got %d", len(orders))
	}
	o := orders[0]
	if o.Type != MsgOrder || o.DeliverySeq != 1 || o.Order == nil || o.Order.AccountID != "acct-1" || o.Order.Lots != 0.10 {
		t.Fatalf("unexpected order envelope: %+v", o)
	}

	// At-least-once: before ack, the same order is returned again.
	if again := longPollGet(t, h.srv.URL, ""); len(again) != 1 || again[0].DeliverySeq != 1 {
		t.Fatalf("unacked order should be re-returned, got %+v", again)
	}

	// Report the fill; the order clears.
	if code := longPollPost(t, h.srv.URL, []Envelope{{Type: MsgResult, DeliverySeq: 1,
		Result: &WireResult{AccountID: "acct-1", Status: delivery.StatusFilled, FinalVolume: 0.10}}}); code != http.StatusOK {
		t.Fatalf("POST result status %d", code)
	}
	if got := longPollGet(t, h.srv.URL, ""); got != nil {
		t.Fatalf("queue should be empty after result, got %+v", got)
	}
	if ep, _ := h.ts.PollEndpoint("conn-1"); ep.ResumeToken() != 1 {
		t.Fatalf("resume token should be 1")
	}
}

// TR-7 auth parity: an unknown key is rejected on the long-poll path too.
func TestLongPollAuthRejected(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	resp, err := http.Get(h.srv.URL + "/agent/longpoll?connection_key=nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for unknown key, got %d", resp.StatusCode)
	}
}

// TR-6/EC-9 on the long-poll path: an unacked order aged past the window is
// dropped with delivery_expired rather than re-returned.
func TestLongPollExpiry(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	longPollGet(t, h.srv.URL, "") // register endpoint
	h.relay.Process(relay.Request{Body: openLongBody("lp-expire")})
	if orders := longPollGet(t, h.srv.URL, ""); len(orders) != 1 {
		t.Fatalf("want 1 queued order, got %d", len(orders))
	}
	h.clk.advance(90 * time.Second) // past the 60s window
	if got := longPollGet(t, h.srv.URL, ""); got != nil {
		t.Fatalf("expired order should not be returned, got %+v", got)
	}
	if h.audit.Count(reason.DeliveryExpired) == 0 {
		t.Fatalf("want delivery_expired audited")
	}
}
