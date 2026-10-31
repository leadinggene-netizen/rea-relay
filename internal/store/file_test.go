package store

import (
	"testing"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
)

// demoConns builds a small config fixture: one subscription and one connection
// with a two-symbol table and a single account.
func demoConns() ([]*connection.Connection, []*connection.Subscription) {
	subs := []*connection.Subscription{{ID: "sub-1", Tier: "pro", MaxConnections: 3}}
	conns := []*connection.Connection{{
		ID: "conn-1", Key: "demo-connection-key", SubscriptionID: "sub-1",
		ExpiryUTC: time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC),
		SymbolTable: map[string]connection.BrokerSymbolSpec{
			"XAUUSD": {BrokerSymbol: "XAUUSD", PipSize: 0.1, PipValuePerLot: 1.0, LotMin: 0.01, LotMax: 100, LotStep: 0.01},
			"BTCUSD": {BrokerSymbol: "BTCUSD", PipSize: 1.0, PipValuePerLot: 1.0, LotMin: 0.01, LotMax: 100, LotStep: 0.01},
		},
		Accounts: []connection.Account{{ID: "acct-1", Broker: "demo", Mode: "demo", Equity: 10000}},
	}}
	return conns, subs
}

// TestConfigRoundTrip writes config, reopens the store from the same dir, and
// checks the values match.
func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	conns, subs := demoConns()
	if err := fs.SaveConfig(conns, subs); err != nil {
		t.Fatalf("save config: %v", err)
	}
	fs.Close()

	fs2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	gotConns, gotSubs, err := fs2.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(gotSubs) != 1 || gotSubs[0].ID != "sub-1" || gotSubs[0].MaxConnections != 3 {
		t.Fatalf("subscriptions mismatch: %+v", gotSubs)
	}
	if len(gotConns) != 1 {
		t.Fatalf("want 1 connection, got %d", len(gotConns))
	}
	c := gotConns[0]
	if c.ID != "conn-1" || c.Key != "demo-connection-key" || c.SubscriptionID != "sub-1" {
		t.Fatalf("connection identity mismatch: %+v", c)
	}
	if !c.ExpiryUTC.Equal(conns[0].ExpiryUTC) {
		t.Fatalf("expiry mismatch: got %v want %v", c.ExpiryUTC, conns[0].ExpiryUTC)
	}
	if len(c.SymbolTable) != 2 {
		t.Fatalf("want 2 symbols, got %d", len(c.SymbolTable))
	}
	gold, ok := c.SymbolTable["XAUUSD"]
	if !ok || gold.PipSize != 0.1 || gold.LotStep != 0.01 {
		t.Fatalf("XAUUSD spec mismatch: %+v (ok=%v)", gold, ok)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].ID != "acct-1" || c.Accounts[0].Equity != 10000 {
		t.Fatalf("account mismatch: %+v", c.Accounts)
	}
}

// TestDedupRestartAndPrune records an idempotency key, reopens (simulating a
// restart), confirms the key is still present (so it would not re-execute), and
// confirms an aged-out key is pruned.
func TestDedupRestartAndPrune(t *testing.T) {
	dir := t.TempDir()
	fs, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	fresh := "conn-1\x00idem-fresh"
	aged := "conn-1\x00idem-aged"
	if err := fs.RecordDedup(fresh, now); err != nil {
		t.Fatalf("record fresh: %v", err)
	}
	if err := fs.RecordDedup(aged, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("record aged: %v", err)
	}
	fs.Close()

	// Restart: reopen from the same dir.
	fs2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	m, err := fs2.LoadDedup()
	if err != nil {
		t.Fatalf("load dedup: %v", err)
	}
	if _, ok := m[fresh]; !ok {
		t.Fatal("fresh key must survive restart (would prevent re-execution)")
	}
	if _, ok := m[aged]; !ok {
		t.Fatal("aged key should still be on disk before prune")
	}

	// Prune by the 5-minute window: the aged key goes, the fresh key stays.
	removed, err := fs2.PruneDedup(5*time.Minute, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("want 1 pruned, got %d", removed)
	}
	m2, _ := fs2.LoadDedup()
	if _, ok := m2[aged]; ok {
		t.Fatal("aged key must be pruned")
	}
	if _, ok := m2[fresh]; !ok {
		t.Fatal("fresh key must remain after prune")
	}

	// Prune survives a further reopen.
	fs2.Close()
	fs3, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen3: %v", err)
	}
	defer fs3.Close()
	m3, _ := fs3.LoadDedup()
	if len(m3) != 1 {
		t.Fatalf("want 1 surviving key after prune+reopen, got %d", len(m3))
	}
}

// TestAuditAppendSurvivesReopen appends audit events and confirms they are read
// back, in order, after reopening the store.
func TestAuditAppendSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	fs, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t0 := time.Unix(1_700_000_000, 0).UTC()
	if err := fs.AppendAudit("conn-1", "symbol_unmapped", "no mapping for EURUSD", t0); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := fs.AppendAudit("conn-1", "duplicate_suppressed", "idempotency key repeated", t0.Add(time.Second)); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	fs.Close()

	fs2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	recs, err := fs2.ReadAudit()
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 audit records, got %d", len(recs))
	}
	if recs[0].Code != "symbol_unmapped" || recs[0].ConnectionID != "conn-1" {
		t.Fatalf("record 0 mismatch: %+v", recs[0])
	}
	if recs[1].Code != "duplicate_suppressed" || !recs[1].At.Equal(t0.Add(time.Second)) {
		t.Fatalf("record 1 mismatch: %+v", recs[1])
	}

	// A further append extends, not truncates, the trail.
	if err := fs2.AppendAudit("conn-2", "broker_rejected", "BROKER-10004", t0.Add(2*time.Second)); err != nil {
		t.Fatalf("append 3: %v", err)
	}
	recs2, _ := fs2.ReadAudit()
	if len(recs2) != 3 {
		t.Fatalf("want 3 records after further append, got %d", len(recs2))
	}
}

// TestLoadEmptyDir confirms a fresh data dir loads as empty (no error), which is
// what the seed-on-empty logic in cmd/relay keys off.
func TestLoadEmptyDir(t *testing.T) {
	dir := t.TempDir()
	fs, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fs.Close()
	conns, subs, err := fs.LoadConfig()
	if err != nil {
		t.Fatalf("load empty config: %v", err)
	}
	if len(conns) != 0 || len(subs) != 0 {
		t.Fatalf("fresh dir should load empty, got %d conns %d subs", len(conns), len(subs))
	}
	m, err := fs.LoadDedup()
	if err != nil || len(m) != 0 {
		t.Fatalf("fresh dir dedup should be empty, got %d err=%v", len(m), err)
	}
	recs, err := fs.ReadAudit()
	if err != nil || len(recs) != 0 {
		t.Fatalf("fresh dir audit should be empty, got %d err=%v", len(recs), err)
	}
}
