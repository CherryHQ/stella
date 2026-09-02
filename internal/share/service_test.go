package share_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/library/recally"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestErrInvalidArtifactPathIsInvalidInput(t *testing.T) {
	if !errors.Is(sharepkg.ErrInvalidArtifactPath, sharepkg.ErrInvalidInput) {
		t.Fatal("ErrInvalidArtifactPath must wrap ErrInvalidInput")
	}
}

func TestAuthorizedMethodsEnforceShareOwnership(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
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

func TestPublicContentResolvesByToken(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	home := t.TempDir()
	svc := sharepkg.NewService(q, memorytest.New(), recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// A live token resolves to the stored content as a transport-neutral value —
	// possession of the unguessable token is the grant, no session required.
	liveToken := "live-token"
	if _, err := q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.NewString(), TokenHash: sharepkg.TokenHash(liveToken), UserID: userID, Title: "live", MediaType: "text/html", Content: []byte("live body"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatalf("CreateShare live: %v", err)
	}
	got, err := svc.PublicContent(ctx, liveToken)
	if err != nil {
		t.Fatalf("PublicContent(live) err=%v", err)
	}
	if string(got.Content) != "live body" || got.Title != "live" || got.MediaType != "text/html" {
		t.Fatalf("PublicContent(live) = %+v, want live body/live/text/html", got)
	}
	if got.ExpiresAt == nil {
		t.Fatalf("PublicContent(live).ExpiresAt = nil, want a future time")
	}

	// An expired token is opaque: GetShareByTokenHash filters expiry, so the row
	// is never surfaced through the transport.
	expiredToken := "expired-token"
	if _, err := q.CreateShare(ctx, sqlc.CreateShareParams{ID: uuid.NewString(), TokenHash: sharepkg.TokenHash(expiredToken), UserID: userID, Title: "expired", MediaType: "text/html", Content: []byte("expired body"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(-time.Hour), Valid: true}}); err != nil {
		t.Fatalf("CreateShare expired: %v", err)
	}
	if _, err := svc.PublicContent(ctx, expiredToken); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("PublicContent(expired) err=%v, want ErrNotFound", err)
	}

	// An unknown or empty token is ErrNotFound, so the transport returns a uniform
	// 404 that never distinguishes missing from expired.
	if _, err := svc.PublicContent(ctx, "no-such-token"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("PublicContent(unknown) err=%v, want ErrNotFound", err)
	}
	if _, err := svc.PublicContent(ctx, ""); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("PublicContent(empty) err=%v, want ErrNotFound", err)
	}
}

func TestShareArticleUsesDatabaseBodyWhenMirrorIsMissing(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	store := recally.NewStore(db)
	svc := sharepkg.NewService(q, mem, store, home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
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

func TestShareArtifactNormalizesSemanticRoots(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}), sharepkg.WithAgentAccess(allowAgentRead{}))
	userID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	agentRoot := filepath.Join(home, "users", userID, "agents", agentID)
	userRoot := filepath.Join(home, "users", userID, "data")
	for path, content := range map[string]string{
		filepath.Join(agentRoot, "agent.html"):            "agent",
		filepath.Join(userRoot, "assets", "durable.html"): "asset",
		filepath.Join(userRoot, "shared.html"):            "shared",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	acc := mustAccess(t, svc, agentAuthority(t, userID, agentID))
	for _, tt := range []struct {
		name, path, scope, want string
	}{
		{"home overrides user scope", "$HOME/agent.html", "user", "agent"},
		{"braced home overrides user scope", "${HOME}/agent.html", "user", "agent"},
		{"assets override agent scope", "$STELLA_ASSETS_DIR/durable.html", "agent", "asset"},
		{"workspace mount compatibility", "/workspace/agent.html", "user", "agent"},
		{"user mount compatibility", "/user/assets/durable.html", "agent", "asset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created, err := acc.ShareArtifact(ctx, "session", tt.path, tt.scope, agentID, "7d")
			if err != nil {
				t.Fatalf("ShareArtifact(%q): %v", tt.path, err)
			}
			if got := string(created.Share.Content); got != tt.want {
				t.Errorf("ShareArtifact(%q) content = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
	for _, path := range []string{
		"$HOME/../escape.html",
		"$STELLA_ASSETS_DIR/../shared.html",
		"$TMPDIR/tmp.html",
		"$STELLA_USER_DIR/shared.html",
		"$UNKNOWN/file.html",
		"${HOME",
	} {
		t.Run("reject "+path, func(t *testing.T) {
			_, err := acc.ShareArtifact(ctx, "session", path, "agent", agentID, "7d")
			if !errors.Is(err, sharepkg.ErrInvalidArtifactPath) {
				t.Fatalf("ShareArtifact(%q) err = %v, want invalid artifact path", path, err)
			}
		})
	}
}

func TestShareArtifactRejectsUnsafeAndInvalidFiles(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	mem := memorytest.New()
	home := t.TempDir()
	svc := sharepkg.NewService(q, mem, recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}), sharepkg.WithAgentAccess(allowAgentRead{}))
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
	root := filepath.Join(home, "users", userID, "agents", agentID)
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
	if err := os.WriteFile(filepath.Join(root, "ok.html"), []byte("<p>changed</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "ok.html")); err != nil {
		t.Fatal(err)
	}
	public, err := svc.PublicContent(ctx, created.Token)
	if err != nil {
		t.Fatalf("PublicContent after source removal: %v", err)
	}
	if got := string(public.Content); got != "<p>ok</p>" {
		t.Fatalf("immutable share content = %q, want original snapshot", got)
	}
}

// newShareService builds a share Service backed by the real policy authorizer
// for tests that do not need to reach into recally/assets directly.
func newShareService(t *testing.T, db *pgxpool.Pool) *sharepkg.Service {
	t.Helper()
	home := t.TempDir()
	return sharepkg.NewService(sqlc.New(db), memorytest.New(), recally.NewStore(db), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
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
