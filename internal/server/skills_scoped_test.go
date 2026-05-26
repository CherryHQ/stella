package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/skills"
)

// newNonAdmin creates a non-admin user with a bearer token and returns
// (user, bearer token).
func newNonAdmin(t *testing.T, env *testEnv, username string) (auth.User, string) {
	t.Helper()
	return createTestUserWithToken(t, env.authStore, env.oidcStore, username, auth.RoleUser)
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
func createTestSkill(t *testing.T, env *testEnv, scope string, userID string, agentID, name string) string {
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

func doMultipartRequestWithSession(t *testing.T, srv http.Handler, bearerToken, method, path, fieldName, fileName string, fileData []byte) *httptest.ResponseRecorder {
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
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func decodeSkillList(t *testing.T, rr *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(parseListItems(t, rr), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	return list
}

func findSkill(list []map[string]any, name string) map[string]any {
	for _, item := range list {
		if item["name"] == name {
			return item
		}
	}
	return nil
}

func TestSessionSystemPromptIncludesSkills(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "prompt-skills-agent")
	createTestSkill(t, env, "system", "", "", "inspect-skill")

	sessionID := "prompt-skills-session"
	sm := env.mem.(memory.SessionManager)
	now := time.Now()
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         sessionID,
		AgentID:    agentID,
		UserID:     env.adminUser.ID,
		Channel:    "admin",
		Kind:       "chat",
		OrgID:      env.orgID,
		CreatedAt:  now,
		LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/agents/"+agentID+"/sessions/"+sessionID+"/system-prompt", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var got struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"## Skills", "<name>inspect-skill</name>", "stella skills load <skill-name>"} {
		if !strings.Contains(got.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, got.SystemPrompt)
		}
	}
}

// --- Agent-context skill endpoints ---

func TestAgentSkills_ListVisibleSkills(t *testing.T) {
	env := setupAdmin(t)

	creator, creatorSID := newNonAdmin(t, env, "creator-list")
	_, otherSID := newNonAdmin(t, env, "other-list")

	agentID := createAgentAsUser(t, env, creatorSID, "list-agent")
	createTestSkill(t, env, "system", "", "", "system-skill")
	createTestSkill(t, env, "agent", "", agentID, "agent-skill")
	createTestSkill(t, env, "user", creator.ID, agentID, "creator-user-skill")
	draftID := createTestSkill(t, env, "user", creator.ID, agentID, "draft-skill")
	draftStatus := "draft"
	if err := env.pluginHost.SkillStore().Update(context.Background(), draftID, skills.ViewContext{UserID: creator.ID, AgentID: agentID}, skills.UpdatePatch{Status: &draftStatus}); err != nil {
		t.Fatalf("mark skill draft: %v", err)
	}
	deprecatedID := createTestSkill(t, env, "user", creator.ID, agentID, "deprecated-skill")
	deprecatedStatus := "deprecated"
	if err := env.pluginHost.SkillStore().Update(context.Background(), deprecatedID, skills.ViewContext{UserID: creator.ID, AgentID: agentID}, skills.UpdatePatch{Status: &deprecatedStatus}); err != nil {
		t.Fatalf("mark skill deprecated: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, creatorSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("creator status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	list := decodeSkillList(t, rr)
	for _, name := range []string{"system-skill", "agent-skill", "creator-user-skill", "draft-skill"} {
		if findSkill(list, name) == nil {
			t.Fatalf("creator list missing %q: %#v", name, list)
		}
	}
	if draft := findSkill(list, "draft-skill"); draft["status"] != "draft" {
		t.Fatalf("draft status = %v, want draft", draft["status"])
	}
	if deprecated := findSkill(list, "deprecated-skill"); deprecated != nil {
		t.Fatalf("creator list included deprecated skill: %#v", deprecated)
	}

	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	list = decodeSkillList(t, rr)
	if findSkill(list, "system-skill") == nil || findSkill(list, "agent-skill") == nil {
		t.Fatalf("admin list missing system or agent skill: %#v", list)
	}
	if findSkill(list, "creator-user-skill") != nil {
		t.Fatalf("admin list included another user's skill: %#v", list)
	}

	rr = doRequestWithSession(t, env.srv, otherSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

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

	skID := createTestSkill(t, env, "agent", "", a1, "skill-on-agent1")

	rr := doRequestWithSession(t, env.srv, sid, "GET", "/api/agents/"+a2+"/skills/agent/"+skID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-agent get status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_CreateUpdateDeleteFile(t *testing.T) {
	env := setupAdmin(t)

	creator, sid := newNonAdmin(t, env, "creator-ud")
	_, otherSID := newNonAdmin(t, env, "other-ud")
	agentID := createAgentAsUser(t, env, sid, "ud-agent")

	rr := doRequestWithSession(t, env.srv, otherSID, "POST", "/api/agents/"+agentID+"/skills/agent", map[string]any{
		"name": "other-agent-skill",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other create status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "POST", "/api/agents/"+agentID+"/skills/system", map[string]any{
		"name": "system-skill",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("system create status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "POST", "/api/agents/"+agentID+"/skills/user", map[string]any{
		"name":        "user-skill",
		"description": "personal",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("user create status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	listRR := doRequestWithSession(t, env.srv, sid, "GET", "/api/agents/"+agentID+"/skills", nil)
	userSkill := findSkill(decodeSkillList(t, listRR), "user-skill")
	if userSkill == nil || userSkill["scope"] != "user" || userSkill["user_id"] != creator.ID || userSkill["agent_id"] != agentID {
		t.Fatalf("created user skill = %#v, want user scoped to creator and agent", userSkill)
	}

	skID := createTestSkill(t, env, "agent", "", agentID, "skill-ud")

	rr = doRequestWithSession(t, env.srv, sid, "PATCH", "/api/agents/"+agentID+"/skills/agent/"+skID, map[string]any{
		"description": "updated",
		"files":       map[string]string{"SKILL.md": "# updated body"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/agent/"+skID+"/file?path=reference.md", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete file status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/agent/"+skID+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete SKILL.md status = %d, want 400", rr.Code)
	}
}

func TestAgentSkills_InstallScopedSkill(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-install")
	agentID := createAgentAsUser(t, env, creatorSID, "install-agent")
	source, err := filepath.Abs("../../resources/skills/system/stella")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, creatorSID, "POST", "/api/agents/"+agentID+"/skills/agent/install", map[string]any{
		"source": source,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("creator install status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, creatorSID, "POST", "/api/agents/"+agentID+"/skills/system/install", map[string]any{
		"source": source,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("system install status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UploadZip(t *testing.T) {
	env := setupAdmin(t)

	creator, creatorSID := newNonAdmin(t, env, "creator-upload-agent")
	agentID := createAgentAsUser(t, env, creatorSID, "upload-agent")
	archive := createSkillZip(t, map[string]string{
		"bundle/uploaded-skill/SKILL.md":     "---\nname: uploaded-skill\ndescription: Uploaded user skill\nstatus: draft\ndisable-model-invocation: true\n---\n# Uploaded\n",
		"bundle/uploaded-skill/reference.md": "notes",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), creatorSID, "POST", "/api/agents/"+agentID+"/skills/user/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, creatorSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list uploaded skills status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	uploaded := findSkill(decodeSkillList(t, rr), "uploaded-skill")
	if uploaded == nil {
		t.Fatalf("uploaded skill missing from list")
	}
	if uploaded["scope"] != "user" || uploaded["user_id"] != creator.ID || uploaded["agent_id"] != agentID {
		t.Fatalf("uploaded skill ownership = %#v, want user scoped to creator and agent", uploaded)
	}
	if uploaded["status"] != "draft" {
		t.Fatalf("uploaded status = %v, want draft", uploaded["status"])
	}
	if uploaded["disable_model_invocation"] != true {
		t.Fatalf("uploaded disable_model_invocation = %v, want true", uploaded["disable_model_invocation"])
	}
}

func TestAgentUserSkills_SelfOnly(t *testing.T) {
	env := setupAdmin(t)

	u1, sid1 := newNonAdmin(t, env, "user1")
	u2, sid2 := newNonAdmin(t, env, "user2")
	agentID := createAgentAsUser(t, env, sid1, "shared-user-skill-agent")
	if err := env.authStore.AssignAgent(context.Background(), u2.ID, agentID); err != nil {
		t.Fatalf("assign user2 to agent: %v", err)
	}

	skID1 := createTestSkill(t, env, "user", u1.ID, agentID, "u1-skill")
	createTestSkill(t, env, "user", u2.ID, agentID, "u2-skill")

	rr := doRequestWithSession(t, env.srv, sid1, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("u1 list status = %d, want 200", rr.Code)
	}
	list := decodeSkillList(t, rr)
	if findSkill(list, "u1-skill") == nil {
		t.Fatalf("u1 list missing own skill: %#v", list)
	}
	if findSkill(list, "u2-skill") != nil {
		t.Fatalf("u1 list included u2 skill: %#v", list)
	}

	rr = doRequestWithSession(t, env.srv, sid2, "GET", "/api/agents/"+agentID+"/skills/user/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross status = %d, want 404", rr.Code)
	}

	rr = doRequestWithSession(t, env.srv, sid2, "DELETE", "/api/agents/"+agentID+"/skills/user/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross delete status = %d, want 404", rr.Code)
	}
}

func TestAgentSkills_UploadZipRejectsInvalidExtension(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-ext")
	agentID := createAgentAsUser(t, env, sid, "upload-ext-agent")
	archive := createSkillZip(t, map[string]string{
		"wrapper/uploaded-skill/SKILL.md": "---\nname: uploaded-skill\ndescription: Uploaded profile skill\n---\n# Uploaded\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/agents/"+agentID+"/skills/user/upload", "file", "uploaded-skill.tar", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UploadZipRejectsMissingSkillMD(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-missing")
	agentID := createAgentAsUser(t, env, sid, "upload-missing-agent")
	archive := createSkillZip(t, map[string]string{
		"wrapper/uploaded-skill/reference.md": "reference",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/agents/"+agentID+"/skills/user/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UploadZipRejectsMultipleSkillRoots(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-layout")
	agentID := createAgentAsUser(t, env, sid, "upload-layout-agent")
	archive := createSkillZip(t, map[string]string{
		"wrapper/skill-one/SKILL.md": "---\nname: skill-one\ndescription: one\n---\n# One\n",
		"wrapper/skill-two/SKILL.md": "---\nname: skill-two\ndescription: two\n---\n# Two\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/agents/"+agentID+"/skills/user/upload", "file", "multi.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UploadZipRejectsPathTraversal(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "user-upload-traversal")
	agentID := createAgentAsUser(t, env, sid, "upload-traversal-agent")
	archive := createSkillZip(t, map[string]string{
		"../uploaded-skill/SKILL.md": "---\nname: uploaded-skill\ndescription: Uploaded profile skill\n---\n# Uploaded\n",
	})

	rr := doMultipartRequestWithSession(t, env.srv.Handler(), sid, "POST", "/api/agents/"+agentID+"/skills/user/upload", "file", "uploaded-skill.zip", archive)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}
