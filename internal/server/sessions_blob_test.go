package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// workspaceTestFilesystem is a provider-shaped test double. It has no host
// root, so a passing server test proves workspace handlers use only canonical
// Filesystem coordinates through the runtime callback.
type workspaceTestFilesystem struct {
	files map[string][]byte
	dirs  map[string]bool
	err   error
}

func newWorkspaceTestFilesystem() *workspaceTestFilesystem {
	return &workspaceTestFilesystem{files: map[string][]byte{}, dirs: map[string]bool{"/workspace": true, "/user": true, "/user/assets": true}}
}
func (*workspaceTestFilesystem) Close() error { return nil }
func (f *workspaceTestFilesystem) Read(_ context.Context, name string, options pkgsandbox.ReadOptions) (io.ReadCloser, pkgsandbox.FileInfo, error) {
	if f.err != nil {
		return nil, pkgsandbox.FileInfo{}, f.err
	}
	data, ok := f.files[name]
	if !ok {
		return nil, pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	if int64(len(data)) > options.MaxBytes {
		return io.NopCloser(bytes.NewReader(data[:options.MaxBytes])), pkgsandbox.FileInfo{Size: int64(len(data))}, pkgsandbox.ErrReadLimit
	}
	return io.NopCloser(bytes.NewReader(data)), pkgsandbox.FileInfo{Name: path.Base(name), Size: int64(len(data)), Mode: 0o644, ModTime: time.Unix(1, 0).UTC()}, nil
}

func (f *workspaceTestFilesystem) Write(_ context.Context, name string, reader io.Reader, _ pkgsandbox.WriteOptions) error {
	if f.err != nil {
		return f.err
	}
	data, err := io.ReadAll(reader)
	if err == nil {
		f.files[name] = data
	}
	return err
}

func (f *workspaceTestFilesystem) Upload(ctx context.Context, name string, reader io.Reader, options pkgsandbox.WriteOptions) error {
	return f.Write(ctx, name, reader, options)
}

func (f *workspaceTestFilesystem) Stat(_ context.Context, name string) (pkgsandbox.FileInfo, error) {
	if f.err != nil {
		return pkgsandbox.FileInfo{}, f.err
	}
	if f.dirs[name] {
		return pkgsandbox.FileInfo{Name: path.Base(name), IsDir: true, Mode: fs.ModeDir}, nil
	}
	data, ok := f.files[name]
	if !ok {
		return pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	return pkgsandbox.FileInfo{Name: path.Base(name), Size: int64(len(data)), Mode: 0o644, ModTime: time.Unix(1, 0).UTC()}, nil
}

func (f *workspaceTestFilesystem) List(_ context.Context, directory string) ([]pkgsandbox.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	if !f.dirs[directory] {
		return nil, fs.ErrNotExist
	}
	entries := map[string]pkgsandbox.DirEntry{}
	for name, data := range f.files {
		if rel, ok := strings.CutPrefix(name, directory+"/"); ok && !strings.Contains(rel, "/") {
			entries[rel] = pkgsandbox.DirEntry{Name: rel, Size: int64(len(data))}
		}
	}
	for name := range f.dirs {
		if rel, ok := strings.CutPrefix(name, directory+"/"); ok && !strings.Contains(rel, "/") {
			entries[rel] = pkgsandbox.DirEntry{Name: rel, IsDir: true}
		}
	}
	out := make([]pkgsandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *workspaceTestFilesystem) Mkdir(_ context.Context, name string, _ fs.FileMode) error {
	if f.err != nil {
		return f.err
	}
	for current := name; current != "/"; current = path.Dir(current) {
		f.dirs[current] = true
		if current == "/workspace" || current == "/user" {
			break
		}
	}
	return nil
}

func (f *workspaceTestFilesystem) Remove(_ context.Context, name string, _ bool) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.files[name]; ok {
		delete(f.files, name)
		return nil
	}
	if !f.dirs[name] {
		return fs.ErrNotExist
	}
	for candidate := range f.files {
		if strings.HasPrefix(candidate, name+"/") {
			delete(f.files, candidate)
		}
	}
	return nil
}

func (f *workspaceTestFilesystem) Rename(_ context.Context, oldName, newName string) error {
	if f.err != nil {
		return f.err
	}
	if _, exists := f.files[newName]; exists || f.dirs[newName] {
		return fs.ErrExist
	}
	data, ok := f.files[oldName]
	if !ok {
		return fs.ErrNotExist
	}
	delete(f.files, oldName)
	f.files[newName] = data
	return nil
}

type workspaceTestRuntime struct {
	filesystem pkgsandbox.Filesystem
	calls      int
	info       agentsession.Info
}

func (*workspaceTestRuntime) Chat(context.Context, agent.ChatRequest) <-chan agent.Event { return nil }
func (*workspaceTestRuntime) StopSession(context.Context, string) bool                   { return false }
func (*workspaceTestRuntime) SubscribeSession(string) (<-chan agent.Event, func()) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, func() {}
}
func (*workspaceTestRuntime) SessionLive(string) bool { return false }
func (*workspaceTestRuntime) CompactAuthorizedSession(context.Context, agentsession.Info) (string, error) {
	return "", nil
}

func (r *workspaceTestRuntime) UseFilesystem(ctx context.Context, info agentsession.Info, use func(pkgsandbox.Filesystem) error) error {
	r.calls++
	r.info = info
	return use(r.filesystem)
}

type workspaceTestRuntimeManager struct{ runtime *workspaceTestRuntime }

func (m workspaceTestRuntimeManager) GetService(string) sessionaccess.RuntimeService {
	return m.runtime
}
func (m workspaceTestRuntimeManager) Default() sessionaccess.RuntimeService { return m.runtime }

func workspaceServer(t *testing.T) (*Server, *workspaceTestRuntime) {
	t.Helper()
	ctx := context.Background()
	mem := memorytest.New()
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1", Kind: string(agentsession.KindChat), Channel: string(agentsession.ChannelWeb), CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	if err := store.CreateAgent(ctx, config.Agent{ID: "a1", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(db).CreateConversation(ctx, sqlc.CreateConversationParams{ID: uuid.NewString(), SessionID: "s1", UserID: pgtype.Text{String: "u1", Valid: true}, AgentID: pgtype.Text{String: "a1", Valid: true}, Channel: string(agentsession.ChannelWeb), Kind: string(agentsession.KindChat), LastActive: now}); err != nil {
		t.Fatal(err)
	}
	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := sessionaccess.NewService(mem, db, store, assets.SessionMedia(), agentaccess.NewService(store, appdb.NewAuthStore(db)))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &workspaceTestRuntime{filesystem: newWorkspaceTestFilesystem()}
	if err := sessions.BindRuntimeManager(workspaceTestRuntimeManager{runtime}); err != nil {
		t.Fatal(err)
	}
	return &Server{sessionAccess: sessions, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, runtime
}

func workspaceRequest(method string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, "/", body).WithContext(withAuthInfo(context.Background(), &AuthInfo{UserID: "u1"}))
}

func TestWorkspaceHandlersUseOneExactFilesystemCallback(t *testing.T) {
	s, runtime := workspaceServer(t)
	filesystem := runtime.filesystem.(*workspaceTestFilesystem)
	filesystem.files["/workspace/read.txt"] = []byte("read")
	filesystem.files["/workspace/move.txt"] = []byte("move")
	scope := apitypes.WorkspaceScopeAgent
	operations := []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{"list", func(w *httptest.ResponseRecorder) {
			s.GetSessionWorkspace(w, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetSessionWorkspaceParams{Scope: &scope})
		}},
		{"create", func(w *httptest.ResponseRecorder) {
			s.CreateWorkspaceFile(w, workspaceRequest(http.MethodPost, strings.NewReader(`{"path":"new.txt","content":"new"}`)), "a1", "s1", apiserver.CreateWorkspaceFileParams{Scope: &scope})
		}},
		{"delete", func(w *httptest.ResponseRecorder) {
			s.DeleteWorkspaceFile(w, workspaceRequest(http.MethodDelete, strings.NewReader(`{"path":"new.txt"}`)), "a1", "s1", apiserver.DeleteWorkspaceFileParams{Scope: &scope})
		}},
		{"move", func(w *httptest.ResponseRecorder) {
			s.MoveWorkspaceFile(w, workspaceRequest(http.MethodPatch, strings.NewReader(`{"path":"move.txt","new_path":"moved.txt"}`)), "a1", "s1", apiserver.MoveWorkspaceFileParams{Scope: &scope})
		}},
		{"read", func(w *httptest.ResponseRecorder) {
			s.GetWorkspaceFileContent(w, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "read.txt", Scope: &scope})
		}},
		{"write", func(w *httptest.ResponseRecorder) {
			s.UpdateWorkspaceFileContent(w, workspaceRequest(http.MethodPatch, strings.NewReader(`{"path":"write.txt","content":"write"}`)), "a1", "s1", apiserver.UpdateWorkspaceFileContentParams{Scope: &scope})
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			before := runtime.calls
			response := httptest.NewRecorder()
			operation.call(response)
			if response.Code < 200 || response.Code >= 300 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if operation.name == "list" && (strings.Contains(response.Body.String(), "\"root\"") || strings.Contains(response.Body.String(), "STELLA_HOME")) {
				t.Fatalf("workspace response leaked a coordinate: %s", response.Body.String())
			}
			if runtime.calls != before+1 {
				t.Fatalf("callbacks=%d, want 1", runtime.calls-before)
			}
			if runtime.info.ID != "s1" || runtime.info.AgentID != "a1" {
				t.Fatalf("runtime info=%+v", runtime.info)
			}
		})
	}
}

func TestWorkspaceRejectsForeignSessionAndHostCoordinatesBeforeCallback(t *testing.T) {
	s, runtime := workspaceServer(t)
	scope := apitypes.WorkspaceScopeAgent
	response := httptest.NewRecorder()
	s.GetWorkspaceFileContent(response, workspaceRequest(http.MethodGet, nil), "a1", "foreign", apiserver.GetWorkspaceFileContentParams{Path: "file.txt", Scope: &scope})
	if response.Code != http.StatusNotFound || runtime.calls != 0 {
		t.Fatalf("foreign response=%d callbacks=%d", response.Code, runtime.calls)
	}
	response = httptest.NewRecorder()
	s.GetWorkspaceFileContent(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "/private/stella/file.txt", Scope: &scope})
	if response.Code != http.StatusBadRequest || runtime.calls != 0 {
		t.Fatalf("host response=%d callbacks=%d", response.Code, runtime.calls)
	}
}

func TestWorkspaceAliasProviderErrorAndUploadReadRoundTrip(t *testing.T) {
	s, runtime := workspaceServer(t)
	filesystem := runtime.filesystem.(*workspaceTestFilesystem)
	filesystem.files["/user/assets/202608/alias.txt"] = []byte("alias")
	response := httptest.NewRecorder()
	s.GetWorkspaceFileContent(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: "$STELLA_ASSETS_DIR/202608/alias.txt"})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "alias") {
		t.Fatalf("alias response=%d body=%s", response.Code, response.Body.String())
	}
	filesystem.err = errors.New("provider down")
	response = httptest.NewRecorder()
	s.GetSessionWorkspace(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetSessionWorkspaceParams{})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("provider response=%d", response.Code)
	}
	filesystem.err = nil
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "roundtrip.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("roundtrip")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := workspaceRequest(http.MethodPost, &form)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response = httptest.NewRecorder()
	s.UploadWorkspaceFile(response, request, "a1", "s1")
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	var upload apitypes.WorkspaceUploadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	if upload.Scope != apitypes.WorkspaceScopeUser || !strings.HasPrefix(upload.RelativePath, "assets/") || upload.Path != "$STELLA_ASSETS_DIR/"+strings.TrimPrefix(upload.RelativePath, "assets/") || strings.Contains(upload.Path, "STELLA_HOME") {
		t.Fatalf("upload=%+v", upload)
	}
	raw := true
	response = httptest.NewRecorder()
	s.GetWorkspaceFileContent(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: upload.RelativePath, Scope: &upload.Scope, Raw: &raw})
	if response.Code != http.StatusOK || response.Body.String() != "roundtrip" {
		t.Fatalf("roundtrip status=%d body=%q", response.Code, response.Body.String())
	}
}
