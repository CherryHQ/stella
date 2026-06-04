package memorywrite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func openTestDB(t *testing.T) (*sql.DB, *sqlc.Queries) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, sqlc.New(db)
}

func seedGroup(t *testing.T, q *sqlc.Queries, groupID string) {
	t.Helper()
	_, err := q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{
		ID:               groupID,
		Platform:         "test",
		PlatformGroupID:  "g1",
		PlatformThreadID: "",
	})
	if err != nil {
		t.Fatalf("seed group state: %v", err)
	}
}

func TestSetGroupMemory(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := "grp-abc"
	seedGroup(t, q, groupID)

	if err := memorywrite.SetGroupMemory(ctx, db, q, groupID, "hello group"); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := memorywrite.GetGroupMemory(ctx, q, groupID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "hello group" {
		t.Fatalf("got %q, want %q", got, "hello group")
	}
}

func TestGetGroupMemoryEmpty(t *testing.T) {
	_, q := openTestDB(t)
	ctx := context.Background()

	got, err := memorywrite.GetGroupMemory(ctx, q, "nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSetGroupMemoryVersionIncrement(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := "grp-ver"
	seedGroup(t, q, groupID)

	if err := memorywrite.SetGroupMemory(ctx, db, q, groupID, "v1"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	row1, _ := q.GetGroupMemory(ctx, groupID)

	if err := memorywrite.SetGroupMemory(ctx, db, q, groupID, "v2"); err != nil {
		t.Fatalf("second set: %v", err)
	}
	row2, _ := q.GetGroupMemory(ctx, groupID)

	if row2.Version <= row1.Version {
		t.Fatalf("version should increment: %d <= %d", row2.Version, row1.Version)
	}
}

func TestSetGroupMemoryRequiresGroupID(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()

	if err := memorywrite.SetGroupMemory(ctx, db, q, "", "content"); err == nil {
		t.Fatal("expected error for empty group_id")
	}
}
