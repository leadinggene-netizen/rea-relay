package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
)

// File names under the data directory.
const (
	configFile = "config.json"
	dedupFile  = "dedup.json"
	auditFile  = "audit.jsonl"
)

// FileStore is a file-backed Store. All operations are guarded by a single
// mutex: correctness over throughput, which suits a development/PoC relay.
//
// Config and dedup are written with a write-temp-then-rename strategy so a
// crash mid-write can never leave a torn or partial file on disk (os.Rename is
// atomic within a directory). Audit is append-only JSONL, fsync'd per line.
type FileStore struct {
	dir string

	mu    sync.Mutex
	dedup map[string]time.Time // in-memory authoritative copy of dedup.json
}

// configSnapshot is the on-disk shape of config.json.
type configSnapshot struct {
	Subscriptions []*connection.Subscription `json:"subscriptions"`
	Connections   []*connection.Connection   `json:"connections"`
}

// Open opens (creating if needed) a file-backed store rooted at dir. It loads
// the dedup map into memory; config and audit are read on demand.
func Open(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("store: empty data directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	fs := &FileStore{dir: dir, dedup: map[string]time.Time{}}
	m, err := fs.readDedup()
	if err != nil {
		return nil, err
	}
	fs.dedup = m
	return fs, nil
}

// Close is a no-op for the file store (every write already syncs).
func (fs *FileStore) Close() error { return nil }

func (fs *FileStore) path(name string) string { return filepath.Join(fs.dir, name) }

// --- config -----------------------------------------------------------------

// LoadConfig reads config.json. A missing file is treated as an empty config.
func (fs *FileStore) LoadConfig() ([]*connection.Connection, []*connection.Subscription, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	b, err := os.ReadFile(fs.path(configFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("store: read config: %w", err)
	}
	var snap configSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, nil, fmt.Errorf("store: decode config: %w", err)
	}
	return snap.Connections, snap.Subscriptions, nil
}

// SaveConfig atomically writes the full config snapshot.
func (fs *FileStore) SaveConfig(conns []*connection.Connection, subs []*connection.Subscription) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	snap := configSnapshot{Subscriptions: subs, Connections: conns}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode config: %w", err)
	}
	return fs.writeAtomic(configFile, b)
}

// --- dedup ------------------------------------------------------------------

// readDedup loads dedup.json; a missing file yields an empty map. Caller need
// not hold the lock (used from Open before the store is shared).
func (fs *FileStore) readDedup() (map[string]time.Time, error) {
	b, err := os.ReadFile(fs.path(dedupFile))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return nil, fmt.Errorf("store: read dedup: %w", err)
	}
	raw := map[string]time.Time{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("store: decode dedup: %w", err)
	}
	return raw, nil
}

// LoadDedup returns a copy of the persisted idempotency map.
func (fs *FileStore) LoadDedup() (map[string]time.Time, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make(map[string]time.Time, len(fs.dedup))
	for k, v := range fs.dedup {
		out[k] = v
	}
	return out, nil
}

// RecordDedup persists one fresh idempotency entry. It rewrites dedup.json
// atomically; within a window each unique key is recorded at most once, so the
// rewrite cost is bounded by the number of distinct keys in the window.
func (fs *FileStore) RecordDedup(key string, first time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.dedup[key]; ok {
		return nil // already persisted; keep the earliest first-seen time
	}
	fs.dedup[key] = first
	return fs.flushDedupLocked()
}

// PruneDedup drops entries older than window (relative to now) and rewrites the
// file. Returns how many were removed.
func (fs *FileStore) PruneDedup(window time.Duration, now time.Time) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	removed := 0
	for k, t := range fs.dedup {
		if now.Sub(t) > window {
			delete(fs.dedup, k)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := fs.flushDedupLocked(); err != nil {
		return removed, err
	}
	return removed, nil
}

func (fs *FileStore) flushDedupLocked() error {
	b, err := json.MarshalIndent(fs.dedup, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode dedup: %w", err)
	}
	return fs.writeAtomic(dedupFile, b)
}

// --- audit ------------------------------------------------------------------

// AppendAudit appends one audit line to audit.jsonl and fsyncs it so the record
// is durable even if the process dies immediately after.
func (fs *FileStore) AppendAudit(connID, code, detail string, at time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	rec := AuditRecord{ConnectionID: connID, Code: code, Detail: detail, At: at}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode audit: %w", err)
	}
	f, err := os.OpenFile(fs.path(auditFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("store: open audit: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("store: write audit: %w", err)
	}
	return f.Sync()
}

// ReadAudit reads every audit line back in append order.
func (fs *FileStore) ReadAudit() ([]AuditRecord, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, err := os.Open(fs.path(auditFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: open audit: %w", err)
	}
	defer f.Close()
	var out []AuditRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("store: decode audit line: %w", err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("store: scan audit: %w", err)
	}
	return out, nil
}

// --- atomic write helper ----------------------------------------------------

// writeAtomic writes b to name via a temp file in the same directory, fsyncs
// it, then renames it into place. os.Rename within a directory is atomic, so a
// reader either sees the old file or the complete new one, never a partial.
// Caller holds fs.mu.
func (fs *FileStore) writeAtomic(name string, b []byte) error {
	final := fs.path(name)
	tmp, err := os.CreateTemp(fs.dir, name+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}
