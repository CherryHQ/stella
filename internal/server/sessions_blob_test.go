package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// assetServer builds a Server backed by an asset store whose durable authority
// is remote, so the local filesystem is only a materialization.
func assetServer(t *testing.T, home string, authority blob.Store, mem memory.Provider) *Server {
	t.Helper()
	assets, err := asset.NewStore(home, authority, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(context.Background(), config.Agent{ID: "a1", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	record, err := mem.(memory.SessionManager).LoadInfo(authz.WithAgentID(authz.WithUserID(context.Background(), "u1"), "a1"), "s1")
	if err != nil {
		t.Fatalf("load session fixture: %v", err)
	}
	if _, err := sqlc.New(db).CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: record.ID, UserID: pgtype.Text{String: record.UserID, Valid: true},
		AgentID: pgtype.Text{String: record.AgentID, Valid: true}, Channel: record.Channel, Kind: record.Kind,
		Archived: record.Archived, LastActive: record.LastActive,
	}); err != nil {
		t.Fatalf("create session fixture: %v", err)
	}
	agentAccess := agentaccess.NewService(assetSessionAgents{}, assetSessionAssignments{})
	sessions, err := sessionaccess.NewService(mem, db, store, assets, agentAccess, sessionaccess.WithHomeWorkspace(serverTestWorkspace{root: home}))
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	return &Server{
		assets:        assets,
		sessionAccess: sessions,
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		agentAccess:   agentAccess,
	}
}

// assetSessionAgents/assetSessionAssignments back a real agent PEP with fixed
// fakes so the asset-focused tests exercise the Session PEP's Agent gate without
// standing up the agent tables.
type assetSessionAgents struct{}

func (assetSessionAgents) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{ID: "a1", Scope: config.AgentScopeSystem, Enabled: true}, nil
}
func (assetSessionAgents) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

type assetSessionAssignments struct{}

func (assetSessionAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestGetWorkspaceFileContentRestoresAssetFromAuthorityOnMiss(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserAssetsDir(agent.UserHomeDir(home, "u1")), "202607", "note.txt")
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(context.Background(), key, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	scope := apitypes.WorkspaceScopeUser
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "assets/202607/note.txt", Scope: &scope})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Content != "hello" {
		t.Fatalf("body=%s err=%v", rr.Body.String(), err)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "hello" {
		t.Fatalf("local data=%q err=%v", data, err)
	}
}

// TestGetWorkspaceFileContentAbsoluteHostPathResolvesToUserRoot is the macOS
// non-isolating repro: an uploaded file is referenced in a chat message by its
// absolute host path (no /user mount to strip). The client cannot know the
// scope, so it sends no scope param; the server must recognize the path lies
// inside the user-data root and serve it there.
func TestGetWorkspaceFileContentAbsoluteHostPathResolvesToUserRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "note.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: local})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Content != "hello" {
		t.Fatalf("body=%s err=%v", rr.Body.String(), err)
	}
}

// TestGetWorkspaceFileContentAbsolutePathOutsideRootsRejected asserts an
// absolute path that lies inside neither workspace root is never served.
func TestGetWorkspaceFileContentAbsolutePathOutsideRootsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "/etc/passwd"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "root:") {
		t.Fatalf("served host file contents: %s", rr.Body.String())
	}
}

// TestGetWorkspaceFileContentAbsoluteTraversalRejected asserts an absolute path
// that descends into a workspace root then climbs back out (via ..) resolves,
// after cleaning, to a location outside the root and is rejected — the sibling
// secret is never served.
func TestGetWorkspaceFileContentAbsoluteTraversalRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	// A secret outside both workspace roots (roots live under home/users/u1/).
	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	userData := agent.UserDataDir(agent.UserHomeDir(home, "u1"))
	if err := os.MkdirAll(userData, 0o755); err != nil {
		t.Fatal(err)
	}
	// userData is home/users/u1/data; three ".." climb out to home/secret.txt.
	escape := userData + "/../../../secret.txt"
	s := assetServer(t, home, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: escape})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "top secret") {
		t.Fatalf("served secret outside root: %s", rr.Body.String())
	}
}

// TestGetWorkspaceFileContentSandboxViewUserPathResolves is the isolating-backend
// repro (docker, linux local): an uploaded file is referenced in a chat message
// by its sandbox-view mount path (/user/...), which lives under no host root. The
// client sends it verbatim with no scope; the server must map the /user mount to
// the user-data root and serve the file. This is the Major-1 regression guard —
// before mount mapping the path failed host containment and 404'd.
func TestGetWorkspaceFileContentSandboxViewUserPathResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "note.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "/user/assets/202607/note.txt"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Content != "hello" {
		t.Fatalf("body=%s err=%v", rr.Body.String(), err)
	}
}

// TestGetWorkspaceFileContentSandboxViewMountTraversalRejected asserts a mount
// path that climbs out of its mount with ".." before descending into the other
// mount (/workspace/../user/...) is rejected, not silently re-mapped to the user
// root. Mount matching keys off the leading /workspace segment; the "../user/..."
// remainder is non-local and OpenSafeRoot rejects it.
func TestGetWorkspaceFileContentSandboxViewMountTraversalRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "note.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "/workspace/../user/assets/202607/note.txt"})
	if rr.Code == http.StatusOK {
		t.Fatalf("status=%d body=%s, want rejection", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "hello") {
		t.Fatalf("served file via mount traversal: %s", rr.Body.String())
	}
}

// TestGetWorkspaceFileContentSymlinkedRootAliasResolves pins the symlink
// resolution in containedRel: STELLA_HOME is a symlink alias, so the workspace
// root the server computes differs in symlink form from the canonical host path
// a chat message may carry. The absolute request path (fully resolved) must still
// resolve inside the root. This covers the macOS /var → /private/var reality; it
// fails if resolveExistingSymlinks were removed (raw filepath.Rel would see the
// two forms as disjoint and 404).
func TestGetWorkspaceFileContentSymlinkedRootAliasResolves(t *testing.T) {
	realHome := t.TempDir()
	aliasHome := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(realHome, aliasHome); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("STELLA_HOME", aliasHome)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	// Written through the alias-form root the server computes from STELLA_HOME.
	aliasLocal := filepath.Join(agent.UserDataDir(agent.UserHomeDir(aliasHome, "u1")), "assets", "202607", "note.txt")
	if err := os.MkdirAll(filepath.Dir(aliasLocal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aliasLocal, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The request carries the fully-resolved canonical path, which differs in
	// symlink form from the alias-form root; only symlink resolution reconciles them.
	canonical, err := filepath.EvalSymlinks(aliasLocal)
	if err != nil {
		t.Fatal(err)
	}
	if canonical == aliasLocal {
		t.Skipf("temp dir not symlinked; alias and canonical paths identical")
	}
	s := assetServer(t, aliasHome, nil, mem)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: canonical})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Content != "hello" {
		t.Fatalf("body=%s err=%v", rr.Body.String(), err)
	}
}

func TestGetWorkspaceRawContentUsesConstrainedInlineResponse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "page.html")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("<script>parent.pwned=true</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, nil, mem)
	scope := apitypes.WorkspaceScopeUser
	raw := true
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "page.html", Scope: &scope, Raw: &raw})
	if rr.Code != http.StatusOK || rr.Body.String() != "<script>parent.pwned=true</script>" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	if got := rr.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("Content-Security-Policy=%q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("Content-Disposition=%q", got)
	}
}

// failingPutStore is a blob authority whose writes always fail, used to assert
// that a handler backed by the asset store rolls back local creation when the
// durable write fails.
type failingPutStore struct{ blob.Store }

func (failingPutStore) Put(context.Context, string, io.Reader) error {
	return errors.New("intentional put failure")
}

type failingDeleteStore struct {
	blob.Store
	deleteCalls *int
}

func (f failingDeleteStore) Delete(context.Context, string) error {
	(*f.deleteCalls)++
	return errors.New("intentional delete failure")
}

func TestCreateWorkspaceFilePersistsAssetToAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	scope := apitypes.WorkspaceScopeUser
	body := strings.NewReader(`{"path":"assets/202607/made.txt","content":"created"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.CreateWorkspaceFile(rr, req, "a1", "s1", apiserver.CreateWorkspaceFileParams{Scope: &scope})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "made.txt")
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := remote.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("remote Open: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "created" {
		t.Fatalf("authority data=%q, want created", data)
	}
}

func TestCreateWorkspaceFileRollsBackLocalOnAuthorityFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, failingPutStore{}, mem)
	scope := apitypes.WorkspaceScopeUser
	body := strings.NewReader(`{"path":"assets/202607/made.txt","content":"created"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.CreateWorkspaceFile(rr, req, "a1", "s1", apiserver.CreateWorkspaceFileParams{Scope: &scope})
	if rr.Code == http.StatusCreated {
		t.Fatalf("expected failure status, got %d", rr.Code)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "made.txt")
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("failed create left a local orphan: %v", err)
	}
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

func TestDeleteWorkspaceFileRemovesLocalAndAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "delete.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("delete me"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(context.Background(), key, strings.NewReader("delete me")); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	scope := apitypes.WorkspaceScopeUser
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"path":"assets/202607/delete.txt"}`)).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.DeleteWorkspaceFile(rr, req, "a1", "s1", apiserver.DeleteWorkspaceFileParams{Scope: &scope})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("local file after delete: %v, want not exist", err)
	}
	if _, err := remote.Open(context.Background(), key); !os.IsNotExist(err) {
		t.Fatalf("authority file after delete: %v, want not exist", err)
	}
}

func TestDeleteWorkspaceFileAuthorityFailurePreservesLocalFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(context.Background(), key, strings.NewReader("keep me")); err != nil {
		t.Fatal(err)
	}
	deleteCalls := 0
	s := assetServer(t, home, failingDeleteStore{Store: remote, deleteCalls: &deleteCalls}, mem)
	scope := apitypes.WorkspaceScopeUser
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"path":"assets/202607/keep.txt"}`)).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.DeleteWorkspaceFile(rr, req, "a1", "s1", apiserver.DeleteWorkspaceFileParams{Scope: &scope})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if deleteCalls != 1 {
		t.Fatalf("authority delete calls = %d, want 1", deleteCalls)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "keep me" {
		t.Fatalf("local file after failed delete = %q, err=%v", data, err)
	}
	body, err := remote.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("authority file after failed delete: %v", err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "keep me" {
		t.Fatalf("authority data after failed delete = %q, err=%v", data, err)
	}
}

func TestMoveWorkspaceFileMirrorsAssetToAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	root := agent.UserDataDir(agent.UserHomeDir(home, "u1"))
	oldLocal := filepath.Join(root, "assets", "202607", "old.txt")
	newLocal := filepath.Join(root, "assets", "202607", "new.txt")
	if err := os.MkdirAll(filepath.Dir(oldLocal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldLocal, []byte("moved"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldKey, err := blob.KeyForPath(home, oldLocal)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := blob.KeyForPath(home, newLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(context.Background(), oldKey, strings.NewReader("old remote")); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	scope := apitypes.WorkspaceScopeUser
	body := strings.NewReader(`{"path":"assets/202607/old.txt","new_path":"assets/202607/new.txt"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.MoveWorkspaceFile(rr, req, "a1", "s1", apiserver.MoveWorkspaceFileParams{Scope: &scope})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := remote.Open(context.Background(), oldKey); !os.IsNotExist(err) {
		t.Fatalf("old remote Open err=%v, want not exist", err)
	}
	rc, err := remote.Open(context.Background(), newKey)
	if err != nil {
		t.Fatalf("new remote Open: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "moved" {
		t.Fatalf("new remote data=%q err=%v", data, err)
	}
}

func TestUpdateWorkspaceFileContentMirrorsAssetToAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	scope := apitypes.WorkspaceScopeUser
	body := strings.NewReader(`{"path":"assets/202607/note.txt","content":"new bytes"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.UpdateWorkspaceFileContent(rr, req, "a1", "s1", apiserver.UpdateWorkspaceFileContentParams{Scope: &scope})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607", "note.txt")
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := remote.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("remote Open: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "new bytes" {
		t.Fatalf("remote data=%q err=%v", data, err)
	}
}

func TestGetWorkspaceFileContentRestoreMissLeavesNoAssetDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, lazyMissingStore{}, mem)
	scope := apitypes.WorkspaceScopeUser
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	rr := httptest.NewRecorder()
	s.GetWorkspaceFileContent(rr, req, "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "assets/202607/missing.txt", Scope: &scope})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	dir := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), "assets", "202607")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("restore miss dir err=%v, want not exist", err)
	}
}

func TestUploadWorkspaceFileMirrorsToAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "photo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("upload")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	req := httptest.NewRequest(http.MethodPost, "/", &buf).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.UploadWorkspaceFile(rr, req, "a1", "s1")
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		RelativePath string `json:"relative_path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	local := filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), filepath.FromSlash(body.RelativePath))
	key, err := blob.KeyForPath(home, local)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := remote.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("remote Open(%q): %v", key, err)
	}
	remoteData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(remoteData) != "upload" {
		t.Fatalf("remote data=%q err=%v", remoteData, err)
	}
	localData, err := os.ReadFile(local)
	if err != nil || string(localData) != "upload" {
		t.Fatalf("local data=%q err=%v", localData, err)
	}
}

// TestUploadWorkspaceFileReturnsRelativePathAndScope asserts the upload response
// exposes the workspace-relative path and scope the web chat needs to build a
// working file-content read URL, alongside the sandbox-view path the agent reads.
func TestUploadWorkspaceFileReturnsRelativePathAndScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("upload")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	s := assetServer(t, home, remote, mem)
	req := httptest.NewRequest(http.MethodPost, "/", &buf).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.UploadWorkspaceFile(rr, req, "a1", "s1")
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body apitypes.WorkspaceUploadResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Scope != apitypes.WorkspaceScopeUser {
		t.Fatalf("scope=%q, want %q", body.Scope, apitypes.WorkspaceScopeUser)
	}
	rel := filepath.ToSlash(body.RelativePath)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		t.Fatalf("relative_path=%q, want a non-empty local path", rel)
	}
	if !strings.HasPrefix(rel, "assets/") || !strings.HasSuffix(rel, "-photo.png") {
		t.Fatalf("relative_path=%q, want assets/...-photo.png", rel)
	}
	// The sandbox-view path is the scope root joined with the relative path, so
	// the relative path is always its suffix regardless of sandbox backend.
	if !strings.HasSuffix(filepath.ToSlash(body.Path), rel) {
		t.Fatalf("path=%q does not end with relative_path=%q", body.Path, rel)
	}
}
