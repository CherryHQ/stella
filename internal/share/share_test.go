package share

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type fakeStore struct{ last sqlc.CreateShareParams }

func (f *fakeStore) CreateShare(_ context.Context, arg sqlc.CreateShareParams) (sqlc.Share, error) {
	f.last = arg
	return sqlc.Share{
		ID:        arg.ID,
		Title:     arg.Title,
		MediaType: arg.MediaType,
		Content:   arg.Content,
		ExpiresAt: arg.ExpiresAt,
		CreatedAt: "2026-06-04 00:00:00",
	}, nil
}

type fakeRenderer struct {
	content Content
	err     error
}

func (f fakeRenderer) RenderArticle(context.Context, string, string) (Content, error) {
	return f.content, f.err
}

// SafePath neutralizes traversal by anchoring the cleaned path under root, so
// `..` segments can never escape — the resolved path always stays within root.
func TestSafePathClampsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../escape", "../../etc/passwd", "sub/../../escape", "ok/file.txt"} {
		abs, err := SafePath(root, rel)
		if err != nil {
			t.Errorf("SafePath(%q) unexpected error: %v", rel, err)
			continue
		}
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			t.Errorf("SafePath(%q) = %q escapes root %q", rel, abs, root)
		}
	}
}

func TestMediaTypeAllowlist(t *testing.T) {
	cases := map[string]string{
		"a.html": "text/html; charset=utf-8",
		"a.md":   "text/markdown; charset=utf-8",
		"a.pdf":  "application/pdf",
		"a.svg":  "image/svg+xml",
		"a.exe":  "",
		"a.go":   "",
		"noext":  "",
	}
	for path, want := range cases {
		if got := MediaType(path); got != want {
			t.Errorf("MediaType(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestArtifactContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewService(&fakeStore{}, nil, "http://x")

	c, err := svc.ArtifactContent(root, "report.md")
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if c.Title != "report.md" || !strings.HasPrefix(c.MediaType, "text/markdown") {
		t.Errorf("unexpected content: %+v", c)
	}

	for _, tc := range []struct {
		path string
		want error
	}{
		{"", ErrInvalidPath},
		{"../escape.md", ErrNotFound}, // traversal clamped to root, then missing
		{"missing.md", ErrNotFound},
		{"dir.md", ErrIsDir},
		{"data.bin", ErrUnsupportedType},
	} {
		if _, err := svc.ArtifactContent(root, tc.path); !errors.Is(err, tc.want) {
			t.Errorf("ArtifactContent(%q) = %v, want %v", tc.path, err, tc.want)
		}
	}
}

func TestArtifactContentTooLarge(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, MaxSize+1)
	if err := os.WriteFile(filepath.Join(root, "big.pdf"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(&fakeStore{}, nil, "http://x")
	if _, err := svc.ArtifactContent(root, "big.pdf"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

func TestCreateExpiryAndURL(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, nil, "http://host:8080/")

	res, err := svc.Create(context.Background(), "u1", Content{Title: "t", MediaType: "text/html", Data: []byte("x")}, "1h")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.last.UserID != "u1" || store.last.Title != "t" {
		t.Errorf("CreateShare params not propagated: %+v", store.last)
	}
	if !store.last.ExpiresAt.Valid {
		t.Error("expected expiry set for 1h")
	}
	if got := svc.PublicURL(res.Token); got != "http://host:8080/s/"+res.Token {
		t.Errorf("PublicURL = %q", got)
	}

	if _, err := svc.Create(context.Background(), "u1", Content{}, "never"); err != nil {
		t.Fatalf("never expiry: %v", err)
	}
	if store.last.ExpiresAt.Valid {
		t.Error("expected no expiry for never")
	}

	if _, err := svc.Create(context.Background(), "u1", Content{}, "bogus"); !errors.Is(err, ErrInvalidExpiry) {
		t.Errorf("want ErrInvalidExpiry, got %v", err)
	}
}

func TestCreateArticleShare(t *testing.T) {
	store := &fakeStore{}

	// nil renderer → ErrArticles.
	if _, err := NewService(store, nil, "http://x").CreateArticleShare(context.Background(), "u1", "a1", "7d"); !errors.Is(err, ErrArticles) {
		t.Errorf("nil renderer: want ErrArticles, got %v", err)
	}

	// renderer rejects (e.g. cross-user) → error propagates.
	denied := NewService(store, fakeRenderer{err: ErrForbidden}, "http://x")
	if _, err := denied.CreateArticleShare(context.Background(), "u1", "a1", "7d"); !errors.Is(err, ErrForbidden) {
		t.Errorf("forbidden: want ErrForbidden, got %v", err)
	}

	// happy path.
	ok := NewService(store, fakeRenderer{content: Content{Title: "Art", MediaType: "text/html; charset=utf-8", Data: []byte("<p>")}}, "http://x")
	res, err := ok.CreateArticleShare(context.Background(), "u1", "a1", "7d")
	if err != nil {
		t.Fatalf("happy: %v", err)
	}
	if res.Title != "Art" || store.last.UserID != "u1" {
		t.Errorf("unexpected result/params: res=%+v params=%+v", res, store.last)
	}
}
