package share_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestAuthorizedMethodsEnforceShareOwnership(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), recally.NewFileManager(home), home, "http://stella.test")
	owner := uuid.NewString()
	foreign := uuid.NewString()
	for _, userID := range []string{owner, foreign} {
		if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	share, err := q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.NewString(), TokenHash: "hash", UserID: owner, Title: "owner", MediaType: "text/html", Content: []byte("owner"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if got, err := svc.As(ident(foreign, "agent")).List(ctx, 10, 0); err != nil || len(got.Shares) != 0 {
		t.Fatalf("foreign List got=%+v err=%v, want empty", got, err)
	}
	if err := svc.As(ident(foreign, "agent")).Revoke(ctx, share.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Revoke err=%v, want ErrNotFound", err)
	}
	if err := svc.As(authz.Identity{}).Revoke(ctx, share.ID); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("unauth Revoke err=%v, want ErrUnauthenticated", err)
	}
}

func TestShareArticleUsesDatabaseBodyWhenMirrorIsMissing(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	store := recally.NewStore(db)
	files := recally.NewFileManager(home)
	svc := sharepkg.NewService(q, mem, store, files, home, "http://stella.test")
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	recallySvc := recally.NewService(store, files, home)
	saved, err := recallySvc.As(authz.Identity{UserID: userID}).Save(ctx, recally.SaveRequest{URL: "https://example.com/share", Title: "Share", Content: "share body"})
	if err != nil {
		t.Fatalf("Save article: %v", err)
	}
	path := saved.Article.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}

	created, err := svc.As(ident(userID, "agent")).ShareArticle(ctx, saved.Article.ID, "7d")
	if err != nil {
		t.Fatalf("ShareArticle: %v", err)
	}
	if !strings.Contains(string(created.Share.Content), "share body") {
		t.Fatalf("shared content %q, want article body", string(created.Share.Content))
	}
}

func TestShareArtifactRestoresAssetFromBlobOnMiss(t *testing.T) {
	defer blob.ResetDefaultForTest()
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
		t.Fatal(err)
	}
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), recally.NewFileManager(home), home, "http://stella.test")
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, userID)), "assets", "202607", "restored.html")
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(ctx, key, strings.NewReader("restored")); err != nil {
		t.Fatalf("remote Put: %v", err)
	}
	created, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "session", "assets/202607/restored.html", "user", "7d")
	if err != nil {
		t.Fatalf("ShareArtifact: %v", err)
	}
	if string(created.Share.Content) != "restored" {
		t.Fatalf("content = %q, want restored", created.Share.Content)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "restored" {
		t.Fatalf("local restored data=%q err=%v", data, err)
	}
}

func TestShareArtifactRestoreMissLeavesNoAssetDir(t *testing.T) {
	defer blob.ResetDefaultForTest()
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	if err := blob.SetDefault(lazyMissingStore{}); err != nil {
		t.Fatal(err)
	}
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), recally.NewFileManager(home), home, "http://stella.test")
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "session", "assets/202607/missing.html", "user", "7d")
	if !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("ShareArtifact err=%v, want ErrNotFound", err)
	}
	dir := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, userID)), "assets", "202607")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("restore miss dir err=%v, want not exist", err)
	}
}

func TestShareArtifactRejectsUnsafeAndInvalidFiles(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), recally.NewFileManager(home), home, "http://stella.test")
	userID := uuid.NewString()
	foreignUser := uuid.NewString()
	agentID := uuid.NewString()
	foreignAgent := uuid.NewString()
	for _, id := range []string{userID, foreignUser} {
		if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, id, id+"@example.com"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	for _, id := range []string{agentID, foreignAgent} {
		if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, $2, '/tmp')`, id, id); err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "owner-session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "foreign-session", UserID: foreignUser, AgentID: foreignAgent}); err != nil {
		t.Fatal(err)
	}
	root := agent.UserAgentDir(home, userID, agentID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.html"), []byte("<p>ok</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "escape.html")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", "../escape.html", "", "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("../escape err=%v, want ErrNotFound clamp", err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", outside, "", "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("absolute path err=%v, want ErrNotFound clamp", err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", "bad.exe", "", "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("missing unsupported file err=%v, want ErrNotFound before type", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", "bad.exe", "", "7d"); !errors.Is(err, sharepkg.ErrUnsupportedType) {
		t.Fatalf("unsupported err=%v, want ErrUnsupportedType", err)
	}
	bigPath := filepath.Join(root, "big.html")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("x", sharepkg.MaxShareSize+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", "big.html", "", "7d"); !errors.Is(err, sharepkg.ErrTooLarge) {
		t.Fatalf("too large err=%v, want ErrTooLarge", err)
	}
	if _, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "foreign-session", "ok.html", "", "7d"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign session err=%v, want ErrForbidden", err)
	}
	created, err := svc.As(ident(userID, agentID)).ShareArtifact(ctx, "owner-session", "ok.html", "", "7d")
	if err != nil {
		t.Fatalf("ShareArtifact ok: %v", err)
	}
	if created.URL == "" || created.Token == "" {
		t.Fatalf("created missing URL/token: %+v", created)
	}
}

type lazyMissingStore struct{}

func (lazyMissingStore) Put(context.Context, string, io.Reader) error { return nil }
func (lazyMissingStore) Delete(context.Context, string) error         { return nil }
func (lazyMissingStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(lazyMissingReader{}), nil
}

type lazyMissingReader struct{}

func (lazyMissingReader) Read([]byte) (int, error) { return 0, os.ErrNotExist }

func ident(userID, agentID string) authz.Identity {
	return authz.Identity{UserID: userID, AgentID: agentID, AgentScoped: agentID != ""}
}
