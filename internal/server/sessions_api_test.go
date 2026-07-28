package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
)

func TestSessionAPILifecycle(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session API Agent")

	rr := doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/sessions", map[string]string{"kind": "chat"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST session status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var session apitypes.Session
	if err := json.Unmarshal(resp.Data, &session); err != nil {
		t.Fatalf("unmarshal created session: %v", err)
	}
	if session.Id == "" || session.AgentId != agentID {
		t.Fatalf("created session = %+v", session)
	}
	sessionPath := "/api/agents/" + agentID + "/sessions/" + session.Id

	// A separate GET proves the created row and its scope survive beyond the
	// request that created it.
	rr = doRequest(t, env, http.MethodGet, sessionPath, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET session status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodPatch, sessionPath, map[string]string{"title": "New title"})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH session status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	session = apitypes.Session{}
	if err := json.Unmarshal(resp.Data, &session); err != nil {
		t.Fatalf("unmarshal updated session: %v", err)
	}
	if session.Title != "New title" {
		t.Fatalf("session title = %q, want New title", session.Title)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), session.Id) {
		t.Fatalf("GET sessions did not return created session: status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodDelete, sessionPath, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE session status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	var archived bool
	if err := env.db.QueryRow(context.Background(), `SELECT archived FROM ctx_conversation WHERE session_id = $1`, session.Id).Scan(&archived); err != nil {
		t.Fatalf("load archived flag: %v", err)
	}
	if !archived {
		t.Fatalf("archived = %v, want true", archived)
	}
	rr = doRequest(t, env, http.MethodGet, sessionPath, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET archived session status = %d, want %d", rr.Code, http.StatusOK)
	}
	session = apitypes.Session{}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &session); err != nil {
		t.Fatalf("unmarshal archived session: %v", err)
	}
	if !session.Archived {
		t.Fatalf("GET archived session returned archived=%v, want true", session.Archived)
	}
}

func TestWorkspaceFileAPILifecycleAndIsolation(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Workspace API Agent")

	rr := doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/sessions", map[string]string{"kind": "chat"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST session status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var session apitypes.Session
	if err := json.Unmarshal(parseResponse(t, rr).Data, &session); err != nil {
		t.Fatalf("decode created session: %v", err)
	}
	workspacePath := "/api/agents/" + agentID + "/sessions/" + session.Id + "/workspace"

	const (
		originalPath = "release.txt"
		movedPath    = "release-moved.txt"
		initialBody  = "before"
		updatedBody  = "after release workspace update"
	)
	rr = doRequest(t, env, http.MethodPost, workspacePath+"/files?scope=agent", map[string]any{
		"path": originalPath, "content": initialBody,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST workspace file status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	fileURL := workspacePath + "/file-content?scope=agent&path=" + url.QueryEscape(originalPath)
	rr = doRequest(t, env, http.MethodGet, fileURL, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET workspace file status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var file apitypes.WorkspaceFileContent
	if err := json.Unmarshal(parseResponse(t, rr).Data, &file); err != nil {
		t.Fatalf("decode workspace file: %v", err)
	}
	if file.Path != originalPath || file.Content != initialBody {
		t.Fatalf("workspace file = %+v, want %q with initial content", file, originalPath)
	}

	rr = doRequest(t, env, http.MethodPatch, workspacePath+"/file-content?scope=agent", map[string]string{
		"path": originalPath, "content": updatedBody,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH workspace content status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	file = apitypes.WorkspaceFileContent{}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &file); err != nil {
		t.Fatalf("decode updated workspace file: %v", err)
	}
	if file.Content != updatedBody {
		t.Fatalf("updated workspace content = %q, want %q", file.Content, updatedBody)
	}

	rr = doRequest(t, env, http.MethodPatch, workspacePath+"/files?scope=agent", map[string]string{
		"path": originalPath, "new_path": movedPath,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH workspace path status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Uploads intentionally land in the shared user workspace. Exercise the
	// multipart wire contract and then prove the returned file is discoverable
	// through that scope rather than merely trusting the upload response.
	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	part, err := writer.CreateFormFile("file", "release-upload.txt")
	if err != nil {
		t.Fatalf("create upload form file: %v", err)
	}
	if _, err := part.Write([]byte("uploaded release asset")); err != nil {
		t.Fatalf("write upload form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close upload form: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, workspacePath+"/upload", &uploadBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	addTestSessionCredential(req, env.bearerToken)
	rr = httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST workspace upload status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var upload apitypes.WorkspaceUploadResponse
	if err := json.Unmarshal(parseResponse(t, rr).Data, &upload); err != nil {
		t.Fatalf("decode workspace upload: %v", err)
	}
	if upload.Path == "" || !strings.Contains(upload.Path, "release-upload.txt") {
		t.Fatalf("uploaded workspace path = %q, want generated release-upload.txt path", upload.Path)
	}

	rr = doRequest(t, env, http.MethodGet, workspacePath+"?scope=agent&show_hidden=true&depth=4", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET agent workspace status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var agentWorkspace apitypes.SessionWorkspace
	if err := json.Unmarshal(parseResponse(t, rr).Data, &agentWorkspace); err != nil {
		t.Fatalf("decode agent workspace: %v", err)
	}
	if !containsString(agentWorkspace.Paths, movedPath) || agentWorkspace.TotalFiles != 1 || agentWorkspace.TotalBytes != int64(len(updatedBody)) {
		t.Fatalf("agent workspace = %+v, want moved file and exact file/byte totals", agentWorkspace)
	}

	rr = doRequest(t, env, http.MethodGet, workspacePath+"?scope=user&show_hidden=true&depth=4", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "release-upload.txt") {
		t.Fatalf("GET user workspace did not return uploaded file: status=%d body=%s", rr.Code, rr.Body.String())
	}

	// SafePath must reject escape attempts before any filesystem operation.
	rr = doRequest(t, env, http.MethodPost, workspacePath+"/files?scope=agent", map[string]string{
		"path": "../escape.txt", "content": "must not escape",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST traversal workspace path status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	_, outsiderSession := newNonAdmin(t, env, "workspace-outsider")
	movedFileURL := workspacePath + "/file-content?scope=agent&path=" + url.QueryEscape(movedPath)
	rr = doRequestWithSession(t, env.srv, outsiderSession, http.MethodGet, movedFileURL, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign GET workspace file status = %d, want opaque %d", rr.Code, http.StatusNotFound)
	}

	rr = doRequest(t, env, http.MethodDelete, workspacePath+"/files?scope=agent", map[string]string{"path": movedPath})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace file status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodGet, movedFileURL, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET deleted workspace file status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// addTestSessionCredential mirrors the shared JSON request helper for the one
// multipart request above without weakening the production authentication path.
func addTestSessionCredential(req *http.Request, sessionToken string) {
	if strings.HasPrefix(sessionToken, "stella_") {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		return
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func TestListSessionsHidesInternalKindsByDefault(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Session List Agent")
	rows := []struct {
		sessionID string
		kind      string
	}{
		{sessionID: "chat-session", kind: "chat"},
		{sessionID: "task-session", kind: "task"},
		{sessionID: "delegate-session", kind: "delegate"},
	}
	for _, row := range rows {
		_, err := env.db.Exec(context.Background(), `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		`, uuid.NewString(), row.sessionID, row.sessionID, row.kind, row.kind, agentID, env.adminUser.ID)
		if err != nil {
			t.Fatalf("seed %s conversation: %v", row.kind, err)
		}
	}

	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET sessions status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list apitypes.SessionList
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal session list: %v", err)
	}
	for _, session := range list.Sessions {
		if session.Kind == "task" || session.Kind == "delegate" {
			t.Fatalf("internal session %q kind %q leaked into default list", session.Id, session.Kind)
		}
	}

	rr = doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions?kind=task", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET task sessions status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	list = apitypes.SessionList{}
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal task session list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Id != "task-session" {
		t.Fatalf("task-filtered sessions = %#v, want task-session only", list.Sessions)
	}
}
