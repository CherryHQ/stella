package reflect

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/vaayne/anna/pkg/db/sqlc"
	_ "modernc.org/sqlite"
)

func newTestWatermarkStore(t *testing.T) *watermarkStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE reflect_watermarks (
		session_id TEXT NOT NULL PRIMARY KEY,
		reviewed_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	return newWatermarkStore(sqlc.New(db))
}

func TestWatermarkStore_GetMissing(t *testing.T) {
	ws := newTestWatermarkStore(t)
	ts, err := ws.get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for missing key, got %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time for missing key, got %v", ts)
	}
}

func TestWatermarkStore_SetAndGet(t *testing.T) {
	ws := newTestWatermarkStore(t)
	ctx := context.Background()

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", now); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("expected %v, got %v", now, got)
	}
}

func TestWatermarkStore_Upsert(t *testing.T) {
	ws := newTestWatermarkStore(t)
	ctx := context.Background()

	t1 := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 6, 13, 0, 0, 0, time.UTC)

	if err := ws.set(ctx, "s1", t1); err != nil {
		t.Fatal(err)
	}
	if err := ws.set(ctx, "s1", t2); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Errorf("expected upserted value %v, got %v", t2, got)
	}
}
