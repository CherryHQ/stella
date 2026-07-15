package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type atomicUpgradeTestStore struct {
	pkgplugins.SkillStore
	existing     []string
	atomicCalls  int
	directWrites int
	deleted      []string
}

func (s *atomicUpgradeTestStore) ListFiles(context.Context, string) ([]string, error) {
	return slices.Clone(s.existing), nil
}

func (s *atomicUpgradeTestStore) UpsertFile(context.Context, string, string, string) error {
	s.directWrites++
	return errors.New("non-atomic upsert called")
}

func (s *atomicUpgradeTestStore) DeleteFile(context.Context, string, string) error {
	s.directWrites++
	return errors.New("non-atomic delete called")
}

func (s *atomicUpgradeTestStore) Update(context.Context, string, pkgplugins.SkillUpdatePatch) error {
	s.directWrites++
	return errors.New("non-atomic update called")
}

func (s *atomicUpgradeTestStore) ApplySkillUpgrade(_ context.Context, _ string, _ map[string]string, deleted []string, _ pkgplugins.SkillUpdatePatch) error {
	s.atomicCalls++
	s.deleted = slices.Clone(deleted)
	return nil
}

func TestGitHubSource(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{"owner/repo@skill", true},
		{"owner/repo", true},
		{"https://github.com/owner/repo", true},
		{"https://github.com/owner/repo/tree/main/path", true},
		{"https://gitlab.com/owner/repo", false},
		{"clawhub:my-skill", false},
		{"clawhub:my-skill@1.0.0", false},
		{"/local/path", false},
		{"./relative", false},
	}
	for _, c := range cases {
		if got := GitHubSource(c.source); got != c.want {
			t.Errorf("GitHubSource(%q) = %v, want %v", c.source, got, c.want)
		}
	}
}

func TestFetchSkillFilesErrorPathNoPanic(t *testing.T) {
	// An error returned after the deferred cleanup guard is installed must not
	// panic: error paths hand back a nil cleanup, and the guard must not call it.
	_, _, _, _, err := FetchSkillFiles(context.Background(), "/nonexistent/stella-skill-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent local path")
	}
}

func TestUpgradeInStoreNoSource(t *testing.T) {
	// A skill with no recorded source can't be upgraded; the guard returns before
	// any store call, so a nil store is safe here.
	for _, md := range []json.RawMessage{nil, json.RawMessage(`{"created-at":"x"}`)} {
		if _, err := UpgradeInStore(context.Background(), nil, "id", md); !errors.Is(err, ErrNoUpgradeSource) {
			t.Errorf("UpgradeInStore(%s) error = %v, want ErrNoUpgradeSource", md, err)
		}
	}
}

func TestUpgradeInStoreRejectsDeprecatedFrontmatterBeforeWrites(t *testing.T) {
	source := t.TempDir()
	main := "---\nname: blocked-upgrade\ndescription: blocked\nstatus: deprecated\n---\n"
	if err := os.WriteFile(filepath.Join(source, pkgplugins.SkillMainFile), []byte(main), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	metadata, _ := json.Marshal(map[string]string{"source": source, "version": "old"})
	store := &atomicUpgradeTestStore{existing: []string{pkgplugins.SkillMainFile}}

	_, err := UpgradeInStore(context.Background(), store, "skill-id", metadata)
	if !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("UpgradeInStore error = %v, want ErrSkillNotMutable", err)
	}
	if store.atomicCalls != 0 || store.directWrites != 0 {
		t.Fatalf("rejected upgrade writes: atomic=%d direct=%d", store.atomicCalls, store.directWrites)
	}
}

func TestUpgradeInStoreUsesAtomicStoreCapability(t *testing.T) {
	source := t.TempDir()
	main := "---\nname: atomic-upgrade\ndescription: updated\nstatus: active\n---\n"
	if err := os.WriteFile(filepath.Join(source, pkgplugins.SkillMainFile), []byte(main), 0o600); err != nil {
		t.Fatalf("write main fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "new.md"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write support fixture: %v", err)
	}
	metadata, _ := json.Marshal(map[string]string{"source": source, "version": "old"})
	store := &atomicUpgradeTestStore{existing: []string{pkgplugins.SkillMainFile, "stale.md"}}

	result, err := UpgradeInStore(context.Background(), store, "skill-id", metadata)
	if err != nil {
		t.Fatalf("UpgradeInStore: %v", err)
	}
	if !result.Updated || store.atomicCalls != 1 || store.directWrites != 0 || !slices.Equal(store.deleted, []string{"stale.md"}) {
		t.Fatalf("atomic upgrade result=%#v calls=%d direct=%d deleted=%#v", result, store.atomicCalls, store.directWrites, store.deleted)
	}
}

func TestGitHubTokenContext(t *testing.T) {
	ctx := context.Background()
	if tok := githubTokenFromContext(ctx); tok != "" {
		t.Errorf("empty ctx token = %q, want empty", tok)
	}
	// Empty token is a no-op.
	if ctx2 := WithGitHubToken(ctx, ""); githubTokenFromContext(ctx2) != "" {
		t.Error("WithGitHubToken(ctx, \"\") should not store a token")
	}
	ctx = WithGitHubToken(ctx, "abc")
	if tok := githubTokenFromContext(ctx); tok != "abc" {
		t.Errorf("ctx token = %q, want abc", tok)
	}
}
