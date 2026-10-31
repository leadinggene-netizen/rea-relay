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

// fakeDedupStore is an in-test DedupPersister that mimics the file store's
// load/record behaviour without touching disk.
type fakeDedupStore struct{ m map[string]time.Time }

func (f *fakeDedupStore) LoadDedup() (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for k, v := range f.m {
		out[k] = v
	}
	return out, nil
}

func (f *fakeDedupStore) RecordDedup(key string, first time.Time) error {
	if _, ok := f.m[key]; !ok {
		f.m[key] = first
	}
	return nil
}

// TestDeduperPersistsAcrossRestart records a fresh key, then builds a new
// Deduper over the same backing (a reboot) and confirms the key is still
// suppressed within the window, while an aged-out entry is dropped on load.
func TestDeduperPersistsAcrossRestart(t *testing.T) {
	now := time.Unix(1000, 0)
	store := &fakeDedupStore{m: map[string]time.Time{}}

	d := NewDeduper(5 * time.Minute)
	d.clock = func() time.Time { return now }
	if err := d.AttachPersister(store); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if d.Seen("c", "idem-1") {
		t.Fatal("first sight should be fresh")
	}
	// A pre-existing aged entry also sits in the backing store.
	store.m["c\x00idem-old"] = now.Add(-10 * time.Minute)

	// Reboot: a brand-new Deduper over the same backing.
	d2 := NewDeduper(5 * time.Minute)
	d2.clock = func() time.Time { return now }
	if err := d2.AttachPersister(store); err != nil {
		t.Fatalf("attach2: %v", err)
	}
	if !d2.Seen("c", "idem-1") {
		t.Fatal("key recorded before restart must still be suppressed (no re-execution)")
	}
	if d2.Seen("c", "idem-old") {
		t.Fatal("aged-out key must be dropped on load, so a new instruction is fresh")
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
