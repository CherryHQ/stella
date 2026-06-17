package db

import "testing"

// TestEmbeddedSmoke proves the Phase 6 backbone end-to-end: an embedded server
// starts, OpenDB migrates the full baseline and ensures FTS against it, and a
// query round-trips. If this passes, every other PG test can rely on the same
// path.
func TestEmbeddedSmoke(t *testing.T) {
	e, err := StartEmbedded("", 0)
	if err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop() })

	db, err := OpenDB(e.DSN())
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var n int
	if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", n)
	}
}
