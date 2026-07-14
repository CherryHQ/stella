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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/asset"
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
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), mustAssets(t, home, nil), home, "http://stella.test")
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
	// A foreign user sees none of the owner's shares and cannot revoke one.
	foreignAcc := mustAccess(t, svc, userAuthority(t, foreign))
	if got, err := foreignAcc.List(ctx, 10, 0); err != nil || len(got.Shares) != 0 {
		t.Fatalf("foreign List got=%+v err=%v, want empty", got, err)
	}
	if err := foreignAcc.Revoke(ctx, share.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Revoke err=%v, want ErrNotFound", err)
	}
}

func TestShareArticleUsesDatabaseBodyWhenMirrorIsMissing(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	store := recally.NewStore(db)
	svc := sharepkg.NewService(q, mem, store, mustAssets(t, home, nil), home, "http://stella.test")
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	recallyAcc, err := recally.NewService(store, home).Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("recally Access: %v", err)
	}
	saved, err := recallyAcc.Save(ctx, recally.SaveRequest{URL: "https://example.com/share", Title: "Share", Content: "share body"})
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

	created, err := mustAccess(t, svc, userAuthority(t, userID)).ShareArticle(ctx, saved.Article.ID, "7d")
	if err != nil {
		t.Fatalf("ShareArticle: %v", err)
	}
	if !strings.Contains(string(created.Share.Content), "share body") {
		t.Fatalf("shared content %q, want article body", string(created.Share.Content))
	}
}

func TestShareArtifactRestoresAssetFromBlobOnMiss(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), mustAssets(t, home, remote), home, "http://stella.test")
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
	created, err := mustAccess(t, svc, agentAuthority(t, userID, agentID)).ShareArtifact(ctx, "session", "assets/202607/restored.html", "user", agentID, "7d")
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
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), mustAssets(t, home, lazyMissingStore{}), home, "http://stella.test")
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	_, err := mustAccess(t, svc, agentAuthority(t, userID, agentID)).ShareArtifact(ctx, "session", "assets/202607/missing.html", "user", agentID, "7d")
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
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), mustAssets(t, home, nil), home, "http://stella.test")
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
	acc := mustAccess(t, svc, agentAuthority(t, userID, agentID))
	if _, err := acc.ShareArtifact(ctx, "owner-session", "../escape.html", "", agentID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("../escape err=%v, want ErrNotFound clamp", err)
	}
	if _, err := acc.ShareArtifact(ctx, "owner-session", outside, "", agentID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("absolute path err=%v, want ErrNotFound clamp", err)
	}
	symlink := filepath.Join(root, "escape-link.html")
	if err := os.Symlink(outside, symlink); err == nil {
		if _, err := acc.ShareArtifact(ctx, "owner-session", "escape-link.html", "", agentID, "7d"); !errors.Is(err, authz.ErrNotFound) {
			t.Fatalf("escaping symlink err=%v, want ErrNotFound", err)
		}
	}
	if _, err := acc.ShareArtifact(ctx, "owner-session", "bad.exe", "", agentID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("missing unsupported file err=%v, want ErrNotFound before type", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.ShareArtifact(ctx, "owner-session", "bad.exe", "", agentID, "7d"); !errors.Is(err, sharepkg.ErrUnsupportedType) {
		t.Fatalf("unsupported err=%v, want ErrUnsupportedType", err)
	}
	bigPath := filepath.Join(root, "big.html")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("x", sharepkg.MaxShareSize+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.ShareArtifact(ctx, "owner-session", "big.html", "", agentID, "7d"); !errors.Is(err, sharepkg.ErrTooLarge) {
		t.Fatalf("too large err=%v, want ErrTooLarge", err)
	}
	if _, err := acc.ShareArtifact(ctx, "foreign-session", "ok.html", "", agentID, "7d"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign session err=%v, want ErrForbidden", err)
	}
	created, err := acc.ShareArtifact(ctx, "owner-session", "ok.html", "", agentID, "7d")
	if err != nil {
		t.Fatalf("ShareArtifact ok: %v", err)
	}
	if created.URL == "" || created.Token == "" {
		t.Fatalf("created missing URL/token: %+v", created)
	}
}

// newShareService builds a share Service backed by the real policy authorizer
// for tests that do not need to reach into recally/assets directly.
func newShareService(t *testing.T, db *pgxpool.Pool) *sharepkg.Service {
	t.Helper()
	home := t.TempDir()
	return sharepkg.NewService(sqlc.New(db), memorytest.New(), recally.NewStore(db), mustAssets(t, home, nil), home, "http://stella.test")
}

// seedShareUser inserts a durable user and returns its id.
func seedShareUser(t *testing.T, db *pgxpool.Pool, suffix string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, id, suffix+"-"+id+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func mustAssets(t *testing.T, home string, authority blob.Store) *asset.Store {
	t.Helper()
	a, err := asset.NewStore(home, authority, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	return a
}

// mustAccess opens exactly one share Access for an authority.
func mustAccess(t *testing.T, svc *sharepkg.Service, authority authz.Authority) *sharepkg.Access {
	t.Helper()
	acc, err := svc.Access(authority)
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	return acc
}

// userAuthority mints a trusted UserActor authority for a durable user.
func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// agentAuthority mints the sole worker authority for a user-owned agent turn.
func agentAuthority(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	return a
}

type lazyMissingStore struct{}

func (lazyMissingStore) Put(context.Context, string, io.Reader) error   { return nil }
func (lazyMissingStore) Delete(context.Context, string) error           { return nil }
func (lazyMissingStore) List(context.Context, string) ([]string, error) { return nil, nil }
func (lazyMissingStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(lazyMissingReader{}), nil
}

type lazyMissingReader struct{}

func (lazyMissingReader) Read([]byte) (int, error) { return 0, os.ErrNotExist }
