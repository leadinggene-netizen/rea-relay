package auth

import (
	"testing"
	"time"
)

func TestSignerReplayRejected(t *testing.T) {
	s := NewSigner(30 * time.Second)
	body := []byte(`{"a":1}`)
	ts := time.Now().Unix()
	sig := Compute("secret", body, ts, "nonce-1")
	m := SignatureMaterial{Signature: sig, Timestamp: ts, Nonce: "nonce-1", Body: body}

	if !s.Verify("secret", m) {
		t.Fatal("first use of a valid signature should verify")
	}
	if s.Verify("secret", m) {
		t.Fatal("replayed nonce must be rejected")
	}
}

func TestSignerSkewAndWrongSecret(t *testing.T) {
	s := NewSigner(30 * time.Second)
	body := []byte(`{}`)
	// Stale timestamp.
	old := time.Now().Add(-10 * time.Minute).Unix()
	if s.Verify("secret", SignatureMaterial{Signature: Compute("secret", body, old, "n"), Timestamp: old, Nonce: "n", Body: body}) {
		t.Fatal("stale timestamp must be rejected")
	}
	// Wrong secret.
	ts := time.Now().Unix()
	if s.Verify("secret", SignatureMaterial{Signature: Compute("other", body, ts, "n2"), Timestamp: ts, Nonce: "n2", Body: body}) {
		t.Fatal("wrong secret must be rejected")
	}
}

func TestDeduperWindow(t *testing.T) {
	d := NewDeduper(time.Minute)
	now := time.Unix(1000, 0)
	d.clock = func() time.Time { return now }
	if d.Seen("c", "k") {
		t.Fatal("first sight should be fresh")
	}
	if !d.Seen("c", "k") {
		t.Fatal("repeat within window should be duplicate")
	}
	// Different connection, same key: independent.
	if d.Seen("c2", "k") {
		t.Fatal("different connection should be fresh")
	}
	// Beyond window: fresh again.
	now = now.Add(2 * time.Minute)
	if d.Seen("c", "k") {
		t.Fatal("beyond window should be fresh again")
	}
}

func TestRateLimiterSheds(t *testing.T) {
	r := NewRateLimiter(1, 3) // burst 3
	now := time.Unix(0, 0)
	r.clock = func() time.Time { return now }
	allowed := 0
	for i := 0; i < 5; i++ {
		if r.Allow("c") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("burst should allow 3, got %d", allowed)
	}
	// After a second, one token refills.
	now = now.Add(time.Second)
	if !r.Allow("c") {
		t.Fatal("a token should refill after 1s")
	}
}
