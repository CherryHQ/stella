package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceViewKeepsTypedPrincipalsDisjointAndSharedRootsReadOnly(t *testing.T) {
	ctx := context.Background()
	r, store := newRegistry(t)
	for _, key := range []Key{SystemSkills(), SystemAgentSkills("a")} {
		if _, err := r.Ensure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(store.base, "users", "abc", "data", "kept")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	user, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "abc", AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "abc", GroupID: "abc", AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if user.Principal.HomeID == group.Principal.HomeID || user.PrincipalRoot == group.PrincipalRoot {
		t.Fatal("equal raw user/group IDs shared a Home")
	}
	after, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("workspace materialization moved existing data")
	}
	if !user.SystemSkillRoot.ReadOnly || !user.SystemAgentSkillRoot.ReadOnly {
		t.Fatal("shared Skill roots must be read-only")
	}
	if _, err := r.WorkspaceView(ctx, WorkspaceRequest{AgentID: "a"}); err != nil {
		t.Fatalf("user-less shared roots: %v", err)
	}
}

func TestWorkspaceViewRejectsNestedSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	r, store := newRegistry(t)
	for _, key := range []Key{SystemSkills(), SystemAgentSkills("a")} {
		if _, err := r.Ensure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	data := filepath.Join(store.base, "users", "u", "data")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, data); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "u", AgentID: "a"}); err == nil {
		t.Fatal("WorkspaceView succeeded through escaping data symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, ".cache")); !os.IsNotExist(err) {
		t.Fatalf("outside store was modified: %v", err)
	}
}

func TestWorkspaceAttachmentRevalidationRejectsForgedAndTombstonedRecords(t *testing.T) {
	ctx := context.Background()
	r, _ := newRegistry(t)
	record, err := r.Ensure(ctx, Principal(UserPrincipal, "u"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forged := range []Record{
		{ID: record.ID, Key: record.Key, StoreID: "other", Locator: record.Locator, State: StateReady},
		{ID: record.ID, Key: Principal(GroupPrincipal, "u"), StoreID: record.StoreID, Locator: record.Locator, State: StateReady},
		{ID: record.ID, Key: record.Key, StoreID: record.StoreID, Locator: "users/other", State: StateReady},
	} {
		if _, err := r.resolveRecord(ctx, forged, false); err == nil {
			t.Fatalf("forged record %+v resolved", forged)
		}
	}
	if _, err := r.Tombstone(ctx, record.Key, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolveRecord(ctx, record, false); err == nil {
		t.Fatal("tombstoned record resolved")
	}
}
