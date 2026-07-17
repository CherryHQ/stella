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
	sessions, err := sessionaccess.NewService(mem, db, store, assets, agentAccess)
	if err != nil {
		t.Fatalf("sessionaccess.NewService: %v", err)
	}
	return &Server{
		mem:           mem,
		store:         store,
		db:            db,
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
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	local := body.Path
	if after, ok := strings.CutPrefix(filepath.ToSlash(body.Path), "/user/"); ok {
		rel := after
		local = filepath.Join(agent.UserDataDir(agent.UserHomeDir(home, "u1")), filepath.FromSlash(rel))
	}
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
