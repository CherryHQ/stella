package lcm_test

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
)

func TestProviderChangelogPageProjectsIdentityReplacementAcrossKeysetPages(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	for _, content := range []string{"profile one", "profile two", "profile three"} {
		if err := profiles.SetProfile(ctx, testUserID, "test", content); err != nil {
			t.Fatalf("set profile %q: %v", content, err)
		}
	}

	reader := memory.ChangelogPageReader(p)
	first, err := reader.ReadChangelogPage(ctx, testUserID, "test", "profile", nil, 1)
	if err != nil {
		t.Fatalf("read first profile changelog page: %v", err)
	}
	if len(first) != 1 || first[0].BeforeText != "profile two" || first[0].AfterText != "profile three" {
		t.Fatalf("first profile page = %+v, want coalesced two-to-three edit", first)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, first[0].CreatedAt)
	if err != nil {
		t.Fatalf("parse first cursor time: %v", err)
	}
	second, err := reader.ReadChangelogPage(ctx, testUserID, "test", "profile", &memory.ChangelogCursor{
		CreatedAt: createdAt, ID: first[0].ID,
	}, 1)
	if err != nil {
		t.Fatalf("read second profile changelog page: %v", err)
	}
	if len(second) != 1 || second[0].ID == first[0].ID || second[0].BeforeText != "profile one" || second[0].AfterText != "profile two" {
		t.Fatalf("second profile page = %+v, want distinct coalesced one-to-two edit", second)
	}

	// The legacy reader remains available for memory tools.
	legacy, err := memory.ChangelogReader(p).ReadChangelog(ctx, testUserID, "test", "profile", 3)
	if err != nil || len(legacy) != 3 {
		t.Fatalf("legacy changelog reader = %+v, err=%v, want three entries", legacy, err)
	}
}
