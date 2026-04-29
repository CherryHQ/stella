package admin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/skills"
)

// newNonAdmin creates a non-admin user with an active session and returns
// (user, session id).
func newNonAdmin(t *testing.T, env *testEnv, username string) (auth.AuthUser, string) {
	t.Helper()
	hash, _ := auth.HashPassword("password")
	u, err := env.authStore.CreateUser(context.Background(), username, hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sid := auth.NewSessionID()
	if _, err := env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sid,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return u, sid
}

// createAgentAsUser creates an agent via the API using the given session
// (so CreatorID is set to that session's user). Returns the agent ID.
func createAgentAsUser(t *testing.T, env *testEnv, sessionID, name string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, "POST", "/api/agents", config.Agent{
		Name:    name,
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent %q: status = %d (body: %s)", name, rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created config.Agent
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return created.ID
}

// createTestSkill creates a skill via the store and returns its ID.
func createTestSkill(t *testing.T, env *testEnv, scope string, userID int64, agentID, name string) string {
	t.Helper()
	sk := skills.Skill{
		Scope:       scope,
		UserID:      userID,
		AgentID:     agentID,
		Name:        name,
		Description: "test",
		Status:      "active",
	}
	id, err := env.pluginHost.SkillStore().Create(context.Background(), sk, map[string]string{
		skills.MainFile: "# " + name,
		"reference.md":  "reference content",
	})
	if err != nil {
		t.Fatalf("Create skill: %v", err)
	}
	return id
}

func createSkillZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func doMultipartRequestWithSession(t *testing.T, srv http.Handler, sessionID, method, path, fieldName, fileName string, fileData []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(fileData); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// --- Agent-scoped endpoints ---

func TestAgentSkills_ListCreatorAccess(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-list")
	_, otherSID := newNonAdmin(t, env, "other-list")

	agentID := createAgentAsUser(t, env, creatorSID, "list-agent")
	createTestSkill(t, env, "agent", 0, agentID, "agent-skill-1")

	// Creator: 200 + 1 skill.
	rr := doRequestWithSession(t, env.srv, creatorSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("creator status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 {
		t.Errorf("creator: got %d skills, want 1", len(list))
	}

	// Another user: 403.
	rr = doRequestWithSession(t, env.srv, otherSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	// Admin: 200.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Unauth: 401.
	rr = doUnauthRequest(t, env.srv, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", rr.Code)
	}
}

func TestAgentSkills_CrossAgentScope(t *testing.T) {
	env := setupAdmin(t)

	_, sid := newNonAdmin(t, env, "creator-cross")
	a1 := createAgentAsUser(t, env, sid, "cross-a1")
	a2 := createAgentAsUser(t, env, sid, "cross-a2")

	skID := createTestSkill(t, env, "agent", 0, a1, "skill-on-agent1")

	// GET /agents/{a2}/skills/{skID} must 404 — skill belongs to a1.
	rr := doRequestWithSession(t, env.srv, sid, "GET", "/api/agents/"+a2+"/skills/"+skID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-agent get status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UpdateDeleteFile(t *testing.T) {
	env := setupAdmin(t)

	_, sid := newNonAdmin(t, env, "creator-ud")
	agentID := createAgentAsUser(t, env, sid, "ud-agent")
	skID := createTestSkill(t, env, "agent", 0, agentID, "skill-ud")

	// Update description.
	desc := "updated"
	rr := doRequestWithSession(t, env.srv, sid, "PUT", "/api/agents/"+agentID+"/skills/"+skID, map[string]any{
		"description": desc,
		"files":       map[string]string{"SKILL.md": "# updated body"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Delete the ref file.
	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/"+skID+"/file?path=reference.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete file status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Cannot delete SKILL.md.
	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/"+skID+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete SKILL.md status = %d, want 400", rr.Code)
	}
}

func TestAgentSkills_InstallAdminOnly(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-install")
	agentID := createAgentAsUser(t, env, creatorSID, "install-agent")
	source, err := filepath.Abs("../../internal/resources/skills/system/anna")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, creatorSID, "POST", "/api/agents/"+agentID+"/skills/install", map[string]any{
		"source": source,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("creator install status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, "POST", "/api/agents/"+agentID+"/skills/install", map[string]any{
		"source": source,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin install status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UploadZipAdminOnly(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-upload-agent")
	agentID := createAgentAsUser(t, env, creatorSID, "upload-agent")
	archive := createSkillZip(t, map[string]string{
		"bundle/uploaded-skill/SKILL.md":     "---\nname: uploaded-skill\ndescription: Uploaded agent skill\nstatus: draft\n---\n# Uploaded\n",
		"bundle/uploaded-skill/reference.md": "notes",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), creatorSID, "POST", "/api/agents/"+agentID+"/skills/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("creator upload status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doMultipartRequestWithSession(t, env.srv.Handler(), env.sessionID, "POST", "/api/agents/"+agentID+"/skills/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin upload status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var created map[string]any
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created["name"] != "uploaded-skill" {
		t.Fatalf("uploaded name = %v, want uploaded-skill", created["name"])
	}

	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list uploaded agent skills status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp = parseResponse(t, rr)
	var list []map[string]any
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 || list[0]["name"] != "uploaded-skill" {
		t.Fatalf("uploaded list = %#v, want uploaded-skill", list)
	}
}

func TestAgentSkills_DuplicateBuiltinAdminOnly(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-duplicate")
	agentID := createAgentAsUser(t, env, creatorSID, "duplicate-agent")

	rr := doRequestWithSession(t, env.srv, creatorSID, "POST", "/api/agents/"+agentID+"/skills/from-builtin/anna", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("creator duplicate status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, "POST", "/api/agents/"+agentID+"/skills/from-builtin/anna", nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin duplicate status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
}

// --- Profile (self-user) endpoints ---

func TestProfileSkills_SelfOnly(t *testing.T) {
	env := setupAdmin(t)

	u1, sid1 := newNonAdmin(t, env, "user1")
	u2, sid2 := newNonAdmin(t, env, "user2")

	skID1 := createTestSkill(t, env, "user", u1.ID, "", "u1-skill")
	_ = createTestSkill(t, env, "user", u2.ID, "", "u2-skill")

	// u1 sees only their own skill.
	rr := doRequestWithSession(t, env.srv, sid1, "GET", "/api/auth/profile/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("u1 list status = %d, want 200", rr.Code)
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 {
		t.Errorf("u1 list: got %d, want 1", len(list))
	}

	// u2 cannot GET u1's skill.
	rr = doRequestWithSession(t, env.srv, sid2, "GET", "/api/auth/profile/skills/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross status = %d, want 404", rr.Code)
	}

	// u2 cannot DELETE u1's skill.
	rr = doRequestWithSession(t, env.srv, sid2, "DELETE", "/api/auth/profile/skills/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross delete status = %d, want 404", rr.Code)
	}
}

func TestProfileSkills_InstallSelf(t *testing.T) {
	env := setupAdmin(t)

	u, sid := newNonAdmin(t, env, "user-install")
	source, err := filepath.Abs("../../internal/resources/skills/system/anna")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, sid, "POST", "/api/auth/profile/skills/install", map[string]any{
		"source": source,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("install status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "GET", "/api/auth/profile/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d skills, want 1", len(list))
	}
	if list[0]["scope"] != "user" {
		t.Fatalf("installed skill scope = %v, want user", list[0]["scope"])
	}
	if got, ok := list[0]["user_id"].(float64); !ok || int64(got) != u.ID {
		t.Fatalf("installed skill user_id = %v, want %d", list[0]["user_id"], u.ID)
	}
}

func TestProfileSkills_UploadZip(t *testing.T) {
	env := setupAdmin(t)

	u, sid := newNonAdmin(t, env, "user-upload")
	archive := createSkillZip(t, map[string]string{
		"wrapper/uploaded-skill/SKILL.md":     "---\nname: uploaded-skill\ndescription: Uploaded profile skill\nstatus: deprecated\ndisable-model-invocation: true\n---\n# Uploaded\n",
		"wrapper/uploaded-skill/reference.md": "reference",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/auth/profile/skills/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "GET", "/api/auth/profile/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d skills, want 1", len(list))
	}
	if list[0]["name"] != "uploaded-skill" {
		t.Fatalf("uploaded skill name = %v, want uploaded-skill", list[0]["name"])
	}
	if list[0]["status"] != "deprecated" {
		t.Fatalf("uploaded skill status = %v, want deprecated", list[0]["status"])
	}
	if list[0]["disable_model_invocation"] != true {
		t.Fatalf("uploaded skill disable_model_invocation = %v, want true", list[0]["disable_model_invocation"])
	}
	if got, ok := list[0]["user_id"].(float64); !ok || int64(got) != u.ID {
		t.Fatalf("uploaded skill user_id = %v, want %d", list[0]["user_id"], u.ID)
	}
}

func TestProfileSkills_UploadZipRejectsInvalidExtension(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-ext")
	archive := createSkillZip(t, map[string]string{
		"wrapper/uploaded-skill/SKILL.md": "---\nname: uploaded-skill\ndescription: Uploaded profile skill\n---\n# Uploaded\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/auth/profile/skills/upload", "file", "uploaded-skill.tar", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestProfileSkills_UploadZipRejectsMissingSkillMD(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-missing")
	archive := createSkillZip(t, map[string]string{
		"wrapper/uploaded-skill/reference.md": "reference",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/auth/profile/skills/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestProfileSkills_UploadZipRejectsMultipleSkillRoots(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-layout")
	archive := createSkillZip(t, map[string]string{
		"wrapper/skill-one/SKILL.md": "---\nname: skill-one\ndescription: one\n---\n# One\n",
		"wrapper/skill-two/SKILL.md": "---\nname: skill-two\ndescription: two\n---\n# Two\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/auth/profile/skills/upload", "file", "multi.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestProfileSkills_UploadZipRejectsPathTraversal(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-traversal")
	archive := createSkillZip(t, map[string]string{
		"../uploaded-skill/SKILL.md": "---\nname: uploaded-skill\ndescription: Uploaded profile skill\n---\n# Uploaded\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/auth/profile/skills/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAdminDeleteSkillFile(t *testing.T) {
	env := setupAdmin(t)

	skID := createTestSkill(t, env, "system", 0, "", "sys-skill")

	rr := doRequest(t, env, "DELETE", "/api/skills/"+skID+"/file?path=reference.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete ref status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// SKILL.md rejected.
	rr = doRequest(t, env, "DELETE", "/api/skills/"+skID+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete SKILL.md status = %d, want 400", rr.Code)
	}
}
