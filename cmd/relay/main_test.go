package main

import (
	"testing"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	filestore "github.com/leadinggene-netizen/rea-relay/internal/store"
)

// TestSeedOnEmptyThenNoReseed verifies that a fresh data dir seeds the demo
// connection, that the seed is persisted, and that a subsequent open of the
// same (now non-empty) dir does NOT re-seed and loads the demo connection back.
func TestSeedOnEmptyThenNoReseed(t *testing.T) {
	dir := t.TempDir()

	// First boot: fresh dir -> seeds.
	st1, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	store1 := connection.NewStore()
	seeded, err := loadOrSeed(st1, store1)
	if err != nil {
		t.Fatalf("loadOrSeed1: %v", err)
	}
	if !seeded {
		t.Fatal("fresh data dir should seed the demo connection")
	}
	if _, ok := store1.Lookup("demo-connection-key"); !ok {
		t.Fatal("demo connection should be present after seeding")
	}
	st1.Close()

	// Second boot: non-empty dir -> does NOT re-seed, demo connection loads.
	st2, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer st2.Close()
	store2 := connection.NewStore()
	seeded2, err := loadOrSeed(st2, store2)
	if err != nil {
		t.Fatalf("loadOrSeed2: %v", err)
	}
	if seeded2 {
		t.Fatal("non-empty data dir must NOT re-seed")
	}
	conn, ok := store2.Lookup("demo-connection-key")
	if !ok {
		t.Fatal("demo connection should load from the persisted config")
	}
	if len(conn.SymbolTable) != 2 || conn.Accounts[0].ID != "acct-1" {
		t.Fatalf("loaded demo connection malformed: %+v", conn)
	}
}

// TestWriteThroughPersistsNewConnection verifies that once the persister is
// attached, adding a connection is written through and survives a reopen.
func TestWriteThroughPersistsNewConnection(t *testing.T) {
	dir := t.TempDir()
	st, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := connection.NewStore()
	if _, err := loadOrSeed(st, store); err != nil {
		t.Fatalf("loadOrSeed: %v", err)
	}
	store.SetPersister(st)

	// A new connection added at runtime must be written through.
	store.AddSubscription(&connection.Subscription{ID: "sub-2", Tier: "basic", MaxConnections: 1})
	store.AddConnection(&connection.Connection{ID: "conn-2", Key: "k2", SubscriptionID: "sub-2"})
	st.Close()

	st2, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	store2 := connection.NewStore()
	if _, err := loadOrSeed(st2, store2); err != nil {
		t.Fatalf("loadOrSeed2: %v", err)
	}
	if _, ok := store2.Lookup("k2"); !ok {
		t.Fatal("runtime-added connection should survive restart via write-through")
	}
	if _, ok := store2.Lookup("demo-connection-key"); !ok {
		t.Fatal("demo connection should still be present")
	}
}
