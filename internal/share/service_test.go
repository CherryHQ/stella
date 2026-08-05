package share

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	access "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type shareFilesystem struct {
	data  map[string][]byte
	infos map[string]pkgsandbox.FileInfo
	err   error
	reads int
}

func (f *shareFilesystem) Close() error { return nil }
func (f *shareFilesystem) Read(_ context.Context, name string, _ pkgsandbox.ReadOptions) (io.ReadCloser, pkgsandbox.FileInfo, error) {
	f.reads++
	if f.err != nil {
		return nil, pkgsandbox.FileInfo{}, f.err
	}
	data, ok := f.data[name]
	if !ok {
		return nil, pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	info, ok := f.infos[name]
	if !ok {
		info = pkgsandbox.FileInfo{Name: name, Size: int64(len(data)), Mode: 0o644}
	}
	return io.NopCloser(bytes.NewReader(data)), info, nil
}

func (*shareFilesystem) Write(context.Context, string, io.Reader, pkgsandbox.WriteOptions) error {
	return errors.New("not used")
}

func (*shareFilesystem) Upload(context.Context, string, io.Reader, pkgsandbox.WriteOptions) error {
	return errors.New("not used")
}

func (*shareFilesystem) Stat(context.Context, string) (pkgsandbox.FileInfo, error) {
	return pkgsandbox.FileInfo{}, fs.ErrNotExist
}

func (*shareFilesystem) List(context.Context, string) ([]pkgsandbox.DirEntry, error) {
	return nil, errors.New("not used")
}

func (*shareFilesystem) Mkdir(context.Context, string, fs.FileMode) error {
	return errors.New("not used")
}
func (*shareFilesystem) Remove(context.Context, string, bool) error   { return errors.New("not used") }
func (*shareFilesystem) Rename(context.Context, string, string) error { return errors.New("not used") }

type shareRuntime struct {
	filesystem pkgsandbox.Filesystem
	calls      int
}

func (r *shareRuntime) Chat(context.Context, agent.ChatRequest) <-chan agent.Event { return nil }
func (r *shareRuntime) SubscribeSession(string) (<-chan agent.Event, func())       { return nil, func() {} }
func (r *shareRuntime) SessionLive(string) bool                                    { return false }
func (r *shareRuntime) CompactAuthorizedSession(context.Context, agentsession.Info) (string, error) {
	return "", nil
}

func (r *shareRuntime) UseFilesystem(ctx context.Context, _ agentsession.Info, use func(pkgsandbox.Filesystem) error) error {
	r.calls++
	return use(r.filesystem)
}

type shareRuntimeManager struct{ runtime *shareRuntime }

func (m shareRuntimeManager) GetService(string) access.RuntimeService { return m.runtime }
func (m shareRuntimeManager) Default() access.RuntimeService          { return m.runtime }

func newShareService(t *testing.T) (*Service, authz.Authority, *shareFilesystem, *shareRuntime) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	owner := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, owner, owner+"@example.com"); err != nil {
		t.Fatal(err)
	}
	store := storepkg.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "a1", Name: "a1", Model: "test", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	now := time.Now().UTC()
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "s1", UserID: owner, AgentID: "a1", Kind: string(agentsession.KindChat), Channel: string(agentsession.ChannelWeb), CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.NewString(), SessionID: "s1", UserID: pgtype.Text{String: owner, Valid: true}, AgentID: pgtype.Text{String: "a1", Valid: true}, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), LastActive: now}); err != nil {
		t.Fatal(err)
	}
	media, err := asset.NewStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := access.NewService(mem, db, store, media.SessionMedia(), agentaccess.NewService(store, appdb.NewAuthStore(db)))
	if err != nil {
		t.Fatal(err)
	}
	filesystem := &shareFilesystem{data: map[string][]byte{}, infos: map[string]pkgsandbox.FileInfo{}}
	runtime := &shareRuntime{filesystem: filesystem}
	if err := sessions.BindRuntimeManager(shareRuntimeManager{runtime}); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(owner), false)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(q, sessions, recally.NewService(recally.NewStore(db), t.TempDir()), "http://stella.test"), authority, filesystem, runtime
}

func TestShareAccessValidatesAndScopesOwnership(t *testing.T) {
	if _, err := (&Service{}).Access(authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("invalid authority error=%v, want forbidden", err)
	}
	svc, authority, _, _ := newShareService(t)
	owner, err := svc.Access(authority)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.create(context.Background(), string(authority.UserID()), "owned", "text/html", []byte("snapshot"), "7d")
	if err != nil {
		t.Fatal(err)
	}
	foreignAuthority, err := authz.NewUserAuthority(authz.UserID(uuid.NewString()), false)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := svc.Access(foreignAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := foreign.List(context.Background(), 10, 0); err != nil || len(got.Shares) != 0 {
		t.Fatalf("foreign List = %+v, %v", got, err)
	}
	if err := foreign.Revoke(context.Background(), created.Share.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Revoke=%v, want not found", err)
	}
	if err := owner.Revoke(context.Background(), created.Share.ID); err != nil {
		t.Fatalf("owner Revoke: %v", err)
	}
}

func TestShareArticleUsesRecallyOwnerAccess(t *testing.T) {
	svc, authority, _, _ := newShareService(t)
	recallyAccess, err := svc.recally.Access(authority)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := recallyAccess.Save(context.Background(), recally.SaveRequest{URL: "https://example.test/article", Title: "Article", Content: "body"})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := svc.Access(authority)
	created, err := owner.ShareArticle(context.Background(), saved.Article.ID, "never")
	if err != nil || created.Share.MediaType != "text/html; charset=utf-8" {
		t.Fatalf("ShareArticle=%+v, %v", created, err)
	}
	foreignAuthority, err := authz.NewUserAuthority(authz.UserID(uuid.NewString()), false)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := svc.Access(foreignAuthority)
	if _, err := foreign.ShareArticle(context.Background(), saved.Article.ID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign ShareArticle=%v, want not found", err)
	}
}

func TestFilesystemMutationIsImmediatelyWorkspaceReadableAndShareable(t *testing.T) {
	ctx := context.Background()
	svc, authority, _, runtime := newShareService(t)
	workspaceRoot, userRoot := t.TempDir(), t.TempDir()
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{
		{Path: pkgsandbox.PathWorkspace, Directory: workspaceRoot},
		{Path: pkgsandbox.PathUser, Directory: userRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = filesystem.Close() }()
	runtime.filesystem = filesystem

	if err := filesystem.Mkdir(ctx, "/user/assets", 0o755); err != nil {
		t.Fatalf("create provider assets directory: %v", err)
	}
	live := []byte("written through the provider filesystem")
	length := int64(len(live))
	if err := filesystem.Write(ctx, "/user/assets/live.html", bytes.NewReader(live), pkgsandbox.WriteOptions{Perm: 0o644, ContentLength: &length}); err != nil {
		t.Fatalf("provider filesystem write: %v", err)
	}

	sessionAccess, err := svc.sessions.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRead, err := sessionAccess.ReadWorkspacePath(ctx, access.WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: access.WorkspaceScopeUser, Path: "assets/live.html"})
	if err != nil || workspaceRead.Content != string(live) {
		t.Fatalf("workspace read = %#v, %v", workspaceRead, err)
	}
	shareAccess, err := svc.Access(authority)
	if err != nil {
		t.Fatal(err)
	}
	created, err := shareAccess.ShareArtifact(ctx, "s1", "$STELLA_ASSETS_DIR/live.html", "agent", "a1", "never")
	if err != nil {
		t.Fatalf("ShareArtifact: %v", err)
	}
	if created.Share.Title != "live.html" || created.Share.MediaType != "text/html; charset=utf-8" || !bytes.Equal(created.Share.Content, live) {
		t.Fatalf("share snapshot = %+v", created.Share)
	}
	mutated := []byte("later provider mutation")
	length = int64(len(mutated))
	if err := filesystem.Write(ctx, "/user/assets/live.html", bytes.NewReader(mutated), pkgsandbox.WriteOptions{Perm: 0o644, ContentLength: &length}); err != nil {
		t.Fatalf("provider filesystem mutation: %v", err)
	}
	public, err := svc.PublicContent(ctx, created.Token)
	if err != nil || !bytes.Equal(public.Content, live) {
		t.Fatalf("immutable public snapshot = %q, %v", public.Content, err)
	}
}

func TestShareArtifactCopiesImmutableSessionSnapshot(t *testing.T) {
	svc, authority, filesystem, runtime := newShareService(t)
	filesystem.data["/workspace/report.html"] = []byte("first")
	created, err := svc.Access(authority)
	if err != nil {
		t.Fatal(err)
	}
	share, err := created.ShareArtifact(context.Background(), "s1", "$HOME/report.html", "user", "a1", "7d")
	if err != nil {
		t.Fatalf("ShareArtifact: %v", err)
	}
	if share.Share.Title != "report.html" || share.Share.MediaType != "text/html; charset=utf-8" || string(share.Share.Content) != "first" {
		t.Fatalf("snapshot = %+v", share.Share)
	}
	if runtime.calls != 1 {
		t.Fatalf("filesystem callbacks=%d, want 1", runtime.calls)
	}
	filesystem.data["/workspace/report.html"] = []byte("mutated")
	public, err := svc.PublicContent(context.Background(), share.Token)
	if err != nil || string(public.Content) != "first" {
		t.Fatalf("immutable public snapshot = %q, %v", public.Content, err)
	}
}

func TestShareArtifactPreservesReadErrorPriority(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		info       pkgsandbox.FileInfo
		data       []byte
		want       error
	}{
		{"missing", "missing.html", pkgsandbox.FileInfo{}, nil, authz.ErrNotFound},
		{"directory", "dir.exe", pkgsandbox.FileInfo{IsDir: true, Mode: fs.ModeDir}, []byte("x"), ErrDirectory},
		{"oversize before type", "large.exe", pkgsandbox.FileInfo{Mode: 0o644, Size: MaxShareSize + 1}, []byte("x"), ErrTooLarge},
		{"unsupported after read", "bad.exe", pkgsandbox.FileInfo{Mode: 0o644, Size: 1}, []byte("x"), ErrUnsupportedType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, authority, filesystem, _ := newShareService(t)
			if tc.data != nil {
				filesystem.data["/workspace/"+tc.path] = tc.data
				filesystem.infos["/workspace/"+tc.path] = tc.info
			}
			acc, _ := svc.Access(authority)
			_, err := acc.ShareArtifact(context.Background(), "s1", tc.path, "agent", "a1", "7d")
			if !errors.Is(err, tc.want) {
				t.Fatalf("ShareArtifact error=%v, want %v", err, tc.want)
			}
		})
	}
}
