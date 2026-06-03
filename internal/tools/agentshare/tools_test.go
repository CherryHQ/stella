package agentshare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type fakeStore struct{ created bool }

func (f *fakeStore) CreateShare(_ context.Context, arg sqlc.CreateShareParams) (sqlc.Share, error) {
	f.created = true
	return sqlc.Share{ID: arg.ID, Title: arg.Title, MediaType: arg.MediaType, ExpiresAt: arg.ExpiresAt, CreatedAt: "2026-06-04 00:00:00"}, nil
}

type fakeRenderer struct {
	content share.Content
	err     error
}

func (f fakeRenderer) RenderArticle(context.Context, string, string) (share.Content, error) {
	return f.content, f.err
}

// newTool builds a tool impl over a temp home with the given renderer, and
// returns the impl plus the resolved workspace dir for (userID, agentID).
func newTool(t *testing.T, r share.ArticleRenderer) (*impl, string) {
	t.Helper()
	home := t.TempDir()
	svc := share.NewService(&fakeStore{}, r, "http://host:9999")
	tl := &impl{svc: svc, home: home}
	// Mirror agent.SetupUserWorkspace layout: home/workspaces/<agent>/users/<user>.
	ws := filepath.Join(home, "workspaces", "a1", "users", "u1")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	return tl, ws
}

func ctxUserAgent(userID, agentID string) context.Context {
	return memory.WithAgentID(memory.WithUserID(context.Background(), userID), agentID)
}

func TestArtifactMissingIdentity(t *testing.T) {
	t.Parallel()
	tl, _ := newTool(t, nil)
	if _, err := tl.artifact(context.Background(), map[string]any{"path": "x.md"}); err == nil {
		t.Error("artifact without identity: want error")
	}
}

func TestArtifactRequiresPath(t *testing.T) {
	t.Parallel()
	tl, _ := newTool(t, nil)
	if _, err := tl.artifact(ctxUserAgent("u1", "a1"), map[string]any{}); err == nil {
		t.Error("artifact without path: want error")
	}
}

func TestArtifactHappyPath(t *testing.T) {
	t.Parallel()
	tl, ws := newTool(t, nil)
	if err := os.WriteFile(filepath.Join(ws, "report.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := tl.artifact(ctxUserAgent("u1", "a1"), map[string]any{"path": "report.md", "expires_in": "1h"})
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	var res struct {
		URL       string `json:"url"`
		Title     string `json:"title"`
		MediaType string `json:"media_type"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if !strings.HasPrefix(res.URL, "http://host:9999/s/") {
		t.Errorf("unexpected url: %q", res.URL)
	}
	if res.Title != "report.md" || !strings.HasPrefix(res.MediaType, "text/markdown") {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.ExpiresAt == "" {
		t.Error("expected expires_at for 1h")
	}
}

func TestArtifactUnsupportedType(t *testing.T) {
	t.Parallel()
	tl, ws := newTool(t, nil)
	if err := os.WriteFile(filepath.Join(ws, "a.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tl.artifact(ctxUserAgent("u1", "a1"), map[string]any{"path": "a.exe"})
	if !errors.Is(err, share.ErrUnsupportedType) {
		t.Errorf("want ErrUnsupportedType, got %v", err)
	}
}

// Traversal cannot reach another user's workspace: the path is clamped to this
// caller's own workspace root, so a sibling user's file is simply not found.
func TestArtifactTraversalCannotEscapeWorkspace(t *testing.T) {
	t.Parallel()
	tl, _ := newTool(t, nil)
	other := filepath.Join(tl.home, "workspaces", "a1", "users", "u2")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "secret.md"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tl.artifact(ctxUserAgent("u1", "a1"), map[string]any{"path": "../u2/secret.md"})
	if !errors.Is(err, share.ErrNotFound) {
		t.Errorf("traversal should clamp to own root (ErrNotFound), got %v", err)
	}
}

func TestArticleHappyAndForbidden(t *testing.T) {
	t.Parallel()
	// forbidden (cross-user) renderer.
	denied, _ := newTool(t, fakeRenderer{err: share.ErrForbidden})
	if _, err := denied.article(ctxUserAgent("u1", "a1"), map[string]any{"article_id": "x"}); !errors.Is(err, share.ErrForbidden) {
		t.Errorf("want ErrForbidden, got %v", err)
	}

	// happy path.
	ok, _ := newTool(t, fakeRenderer{content: share.Content{Title: "Art", MediaType: "text/html; charset=utf-8", Data: []byte("<p>")}})
	out, err := ok.article(ctxUserAgent("u1", "a1"), map[string]any{"article_id": "x"})
	if err != nil {
		t.Fatalf("article: %v", err)
	}
	if !strings.Contains(out, "http://host:9999/s/") || !strings.Contains(out, "Art") {
		t.Errorf("unexpected article output: %s", out)
	}
}

func TestArticleRequiresID(t *testing.T) {
	t.Parallel()
	tl, _ := newTool(t, fakeRenderer{})
	if _, err := tl.article(ctxUserAgent("u1", "a1"), map[string]any{}); err == nil {
		t.Error("article without id: want error")
	}
}
