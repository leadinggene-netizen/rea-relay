// Package auth implements ingestion-time authentication concerns: optional
// HMAC shared-secret signatures with timestamp+nonce replay protection (FR-3,
// NFR-3), idempotency dedup within a window (FR-4), and per-connection inbound
// rate limiting (FR-5). All stores are in-memory and safe for concurrent use.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// SignatureMaterial carries the client-presented signature inputs (NFR-3). The
// canonical signed string is: body + "\n" + timestamp + "\n" + nonce.
type SignatureMaterial struct {
	Signature string // hex-encoded HMAC-SHA256
	Timestamp int64  // unix seconds
	Nonce     string // unique per request
	Body      []byte // raw request body that was signed
}

// Signer verifies HMAC-SHA256 signatures and rejects replays. A signature is
// valid only if (a) it matches HMAC(secret, body|ts|nonce), (b) the timestamp
// is within Skew of now, and (c) the nonce has not been seen within the window.
type Signer struct {
	Skew time.Duration

	mu    sync.Mutex
	seen  map[string]time.Time // nonce -> first-seen time
	clock func() time.Time
}

// NewSigner builds a Signer with the given allowable clock skew.
func NewSigner(skew time.Duration) *Signer {
	return &Signer{Skew: skew, seen: map[string]time.Time{}, clock: time.Now}
}

// Compute returns the canonical HMAC-SHA256 signature for the given inputs.
func Compute(secret string, body []byte, ts int64, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	mac.Write([]byte("\n"))
	mac.Write([]byte(itoa(ts)))
	mac.Write([]byte("\n"))
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the signature, timestamp skew and nonce novelty. It returns
// true only when all three hold. A replayed nonce (within the skew window)
// returns false. Verify records a fresh, valid nonce so it cannot be reused.
func (s *Signer) Verify(secret string, m SignatureMaterial) bool {
	now := s.clock()
	// Timestamp skew check (replay window bound).
	ts := time.Unix(m.Timestamp, 0)
	if d := now.Sub(ts); d > s.Skew || d < -s.Skew {
		return false
	}
	// Constant-time signature comparison.
	want := Compute(secret, m.Body, m.Timestamp, m.Nonce)
	if !hmac.Equal([]byte(want), []byte(m.Signature)) {
		return false
	}
	// Nonce novelty (replay protection).
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc(now)
	if m.Nonce == "" {
		return false
	}
	if _, ok := s.seen[m.Nonce]; ok {
		return false // replay
	}
	s.seen[m.Nonce] = now
	return true
}

// gc drops nonces older than twice the skew window (caller holds the lock).
func (s *Signer) gc(now time.Time) {
	cutoff := now.Add(-2 * s.Skew)
	for n, t := range s.seen {
		if t.Before(cutoff) {
			delete(s.seen, n)
		}
	}
}

// DedupPersister is an optional durable backing for a Deduper (FR-4 across
// restarts). RecordDedup is called write-through the first time a combined key
// is seen within the window; LoadDedup supplies the surviving entries on
// startup. It is satisfied by internal/store's file-backed Store. Defined here
// to keep the dependency direction one-way (store imports auth, not vice versa).
type DedupPersister interface {
	LoadDedup() (map[string]time.Time, error)
	RecordDedup(key string, first time.Time) error
}

// Deduper suppresses repeat idempotency keys within a window, scoped per
// connection (FR-4, EC-8). The first sight of a key within the window is
// "fresh"; repeats are "duplicate". A Deduper can be made durable by attaching
// a DedupPersister so a replayed webhook within the window is still suppressed
// after a process restart.
type Deduper struct {
	Window time.Duration

	mu      sync.Mutex
	seen    map[string]time.Time // connID+"\x00"+key -> first-seen time
	clock   func() time.Time
	persist DedupPersister
}

// NewDeduper builds a Deduper with the given suppression window.
func NewDeduper(window time.Duration) *Deduper {
	return &Deduper{Window: window, seen: map[string]time.Time{}, clock: time.Now}
}

// AttachPersister wires a durable backing store into the Deduper: it loads the
// surviving (non-expired) entries from p into memory and records future fresh
// keys write-through. Entries older than the window are dropped on load so a
// stale key does not suppress a legitimate new instruction after a restart.
func (d *Deduper) AttachPersister(p DedupPersister) error {
	loaded, err := p.LoadDedup()
	if err != nil {
		return err
	}
	now := d.clock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.persist = p
	for k, t := range loaded {
		if now.Sub(t) <= d.Window {
			d.seen[k] = t
		}
	}
	return nil
}

// Seen reports whether (connID, key) was already seen within the window. On a
// fresh key it records it and returns false; on a repeat it returns true
// without extending the window.
func (d *Deduper) Seen(connID, key string) bool {
	now := d.clock()
	d.mu.Lock()
	defer d.mu.Unlock()
	// Evict expired entries.
	for k, t := range d.seen {
		if now.Sub(t) > d.Window {
			delete(d.seen, k)
		}
	}
	id := connID + "\x00" + key
	if t, ok := d.seen[id]; ok && now.Sub(t) <= d.Window {
		return true
	}
	d.seen[id] = now
	if d.persist != nil {
		// Best-effort write-through; the in-memory set remains authoritative
		// for this process, and the store re-prunes by window on load.
		_ = d.persist.RecordDedup(id, now)
	}
	return false
}

// RateLimiter is a per-connection token bucket. Beyond the refill capacity an
// instruction is not lost: the relay still enqueues it (FR-5, EC-13). Allow
// reports whether a token was available without blocking.
type RateLimiter struct {
	rate     float64 // tokens per second
	capacity float64 // burst size

	mu      sync.Mutex
	buckets map[string]*bucket
	clock   func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter with the given sustained rate (per second)
// and burst capacity, applied independently per connection.
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	return &RateLimiter{rate: ratePerSec, capacity: burst, buckets: map[string]*bucket{}, clock: time.Now}
}

// Allow consumes a token for connID if one is available, returning true. When
// no token is available it returns false; the caller decides whether to queue
// or shed (the relay queues).
func (r *RateLimiter) Allow(connID string) bool {
	now := r.clock()
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buckets[connID]
	if b == nil {
		b = &bucket{tokens: r.capacity, last: now}
		r.buckets[connID] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * r.rate
	if b.tokens > r.capacity {
		b.tokens = r.capacity
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// itoa avoids importing strconv for a single call site.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
