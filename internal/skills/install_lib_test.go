package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

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

func TestFetchSkillFilesNormalizesNestedPaths(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "nested-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for filePath, content := range map[string]string{
		filepath.Join(skillDir, MainFile):                "# Nested skill",
		filepath.Join(skillDir, "references", "note.md"): "note",
	} {
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", filePath, err)
		}
	}

	_, files, _, cleanup, err := FetchSkillFiles(context.Background(), skillDir)
	if err != nil {
		t.Fatalf("FetchSkillFiles: %v", err)
	}
	defer cleanup()
	if got := files["references/note.md"]; got != "note" {
		t.Fatalf("nested file content = %q, want note (paths: %v)", got, files)
	}
}

func TestUpgradeInStoreNoSource(t *testing.T) {
	// A skill with no recorded source can't be upgraded; the guard returns before
	// any store call, so a nil store is safe here.
	for _, md := range []json.RawMessage{nil, json.RawMessage(`{"created-at":"x"}`)} {
		if _, err := UpgradeInStore(context.Background(), nil, pkgplugins.Skill{ID: "id", Metadata: md}); !errors.Is(err, ErrNoUpgradeSource) {
			t.Errorf("UpgradeInStore(%s) error = %v, want ErrNoUpgradeSource", md, err)
		}
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
