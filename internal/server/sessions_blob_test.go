package server

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

func TestGetWorkspaceFileContentRestoresAssetFromBlobOnMiss(t *testing.T) {
	defer blob.ResetDefaultForTest()
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
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
	s := &Server{mem: mem, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

type lazyMissingStore struct{}

func (lazyMissingStore) Put(context.Context, string, io.Reader) error   { return nil }
func (lazyMissingStore) Delete(context.Context, string) error           { return nil }
func (lazyMissingStore) List(context.Context, string) ([]string, error) { return nil, nil }
func (lazyMissingStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(lazyMissingReader{}), nil
}

type lazyMissingReader struct{}

func (lazyMissingReader) Read([]byte) (int, error) { return 0, os.ErrNotExist }

func TestMoveWorkspaceFileMirrorsAssetToBlob(t *testing.T) {
	defer blob.ResetDefaultForTest()
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
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
	s := &Server{mem: mem, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

func TestUpdateWorkspaceFileContentMirrorsAssetToBlob(t *testing.T) {
	defer blob.ResetDefaultForTest()
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{mem: mem, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	defer blob.ResetDefaultForTest()
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	if err := blob.SetDefault(lazyMissingStore{}); err != nil {
		t.Fatal(err)
	}
	mem := memorytest.New()
	if err := mem.SaveInfo(context.Background(), memory.SessionInfo{ID: "s1", UserID: "u1", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{mem: mem, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

// A file written between the caller's missing-file check and the install (the
// stat-then-restore window) must win over the restored blob content.
func TestWriteRestoredAssetFileDoesNotReplaceConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "raced.txt")
	if err := os.WriteFile(abs, []byte("fresh-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoredAssetFile(abs, []byte("stale-remote")); err != nil {
		t.Fatalf("writeRestoredAssetFile: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh-local" {
		t.Fatalf("content = %q, want the concurrent local write to win", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stella-restore-") {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
}

func TestUploadWorkspaceFileMirrorsToBlob(t *testing.T) {
	defer blob.ResetDefaultForTest()
	home := t.TempDir()
	t.Setenv("STELLA_HOME", home)
	config.ResetStellaHome()
	defer config.ResetStellaHome()
	remote, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := blob.SetDefault(remote); err != nil {
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
	s := &Server{mem: mem, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	if !filepath.IsAbs(local) {
		rel := strings.TrimPrefix(body.Path, "/user/")
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
