package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/CherryHQ/stella/internal/skill"
)

type agentSkillListResponse struct {
	Skills        []map[string]any `json:"skills"`
	TotalSize     int              `json:"total_size"`
	ScopeCounts   map[string]int   `json:"scope_counts"`
	NextPageToken *string          `json:"next_page_token"`
}

func decodeAgentSkillListResponse(t *testing.T, rrBody json.RawMessage) agentSkillListResponse {
	t.Helper()
	var got agentSkillListResponse
	if err := json.Unmarshal(rrBody, &got); err != nil {
		t.Fatalf("unmarshal agent skill list: %v", err)
	}
	return got
}

func listAgentSkillsLifecycle(t *testing.T, env *testEnv, sid, agentID, query string) agentSkillListResponse {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills"+query, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list agent skills status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	return decodeAgentSkillListResponse(t, parseResponse(t, rr).Data)
}

func skillIDs(items []map[string]any) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		out[id] = true
	}
	return out
}

func TestAgentSkillsLifecycleActiveKeysetAndCompatibility(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-page")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-page-agent")

	wantIDs := map[string]bool{}
	for i := range 13 {
		name := "task5-page-" + string(rune('a'+i))
		wantIDs[createTestSkill(t, env, "user_agent", user.ID, agentID, name)] = true
	}

	defaultPage := listAgentSkillsLifecycle(t, env, sid, agentID, "")
	if len(defaultPage.Skills) != 20 || defaultPage.TotalSize < len(wantIDs) || defaultPage.NextPageToken == nil {
		t.Fatalf("default page = %#v, want 20 results with next token", defaultPage)
	}

	first := listAgentSkillsLifecycle(t, env, sid, agentID, "?scope=user_agent&page_size=12")
	if len(first.Skills) != 12 || first.TotalSize != 13 || first.NextPageToken == nil || *first.NextPageToken == "" {
		t.Fatalf("first page = %#v, want 12/13 with next token", first)
	}
	second := listAgentSkillsLifecycle(t, env, sid, agentID, "?scope=user_agent&page_size=12&page_token="+url.QueryEscape(*first.NextPageToken))
	if len(second.Skills) != 1 || second.TotalSize != 13 || second.NextPageToken != nil {
		t.Fatalf("second page = %#v, want 1/13 without next token", second)
	}
	firstIDs := skillIDs(first.Skills)
	for id := range skillIDs(second.Skills) {
		if firstIDs[id] {
			t.Fatalf("keyset pages repeated skill %s", id)
		}
	}
	for _, query := range []string{
		"?scope=user_agent&page_size=12&page_token=" + url.QueryEscape(mutateOpaqueToken(t, *first.NextPageToken, "kind", "knowledge")),
		"?scope=user_agent&page_size=12&page_token=" + url.QueryEscape(mutateOpaqueToken(t, *first.NextPageToken, "sort_at", nil)),
		"?scope=user_agent&page_size=12&page_token=malformed",
	} {
		rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills"+query, nil)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid lifecycle token query %q status = %d, want 400 (body: %s)", query, rr.Code, rr.Body.String())
		}
	}
	otherAgentID := createAgentAsUser(t, env, sid, "skill-lifecycle-page-other-agent")
	for _, path := range []string{
		"/api/agents/" + agentID + "/skills?scope_group=agent&page_size=12&page_token=" + url.QueryEscape(*first.NextPageToken),
		"/api/agents/" + agentID + "/skills?scope=user_agent&q=other&page_size=12&page_token=" + url.QueryEscape(*first.NextPageToken),
		"/api/agents/" + otherAgentID + "/skills?scope=user_agent&page_size=12&page_token=" + url.QueryEscape(*first.NextPageToken),
	} {
		rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, path, nil)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("cross-query lifecycle token path %q status = %d, want 400 (body: %s)", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAgentSkillsLifecycleScopeCountsAndSearch(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-filter")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-filter-agent")
	ctx := context.Background()

	createTestSkill(t, env, "user", user.ID, "", "task5-filter-manual-user")
	createTestSkill(t, env, "user_agent", user.ID, agentID, "task5-filter-manual-agent")
	reflectSkill, err := env.skillStore.CreateReflectOwnedUserAgentSkill(ctx, skill.ReflectSkillCreate{
		UserID: user.ID, AgentID: agentID, Name: "task5-filter-reflect-agent",
		Description: "Needle Description", MainFileContent: "reflect body",
	})
	if err != nil {
		t.Fatalf("create reflect-owned skill: %v", err)
	}

	// Counts apply search but ignore the selected scope group.
	got := listAgentSkillsLifecycle(t, env, sid, agentID, "?scope_group=user&q=needle&page_size=12")
	if len(got.Skills) != 0 {
		t.Fatalf("user group unexpectedly returned reflect agent skill: %#v", got.Skills)
	}
	if got.TotalSize != 0 || got.ScopeCounts["all"] != 1 || got.ScopeCounts["agent"] != 1 || got.ScopeCounts["user"] != 0 {
		t.Fatalf("filtered counts = %#v total=%d, want all=1 agent=1 selected total=0", got.ScopeCounts, got.TotalSize)
	}

	agent := listAgentSkillsLifecycle(t, env, sid, agentID, "?scope_group=agent&q=NEEDLE&page_size=12")
	if len(agent.Skills) != 1 || agent.Skills[0]["id"] != reflectSkill.ID || agent.Skills[0]["created_by"] != "reflect" {
		t.Fatalf("agent reflect result = %#v, want %s", agent.Skills, reflectSkill.ID)
	}
	if agent.ScopeCounts["all"] != 1 || agent.ScopeCounts["agent"] != 1 {
		t.Fatalf("agent reflect counts = %#v", agent.ScopeCounts)
	}
}

func TestAgentSkillsLifecycleDeleteUsesStableIDAndActiveName(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-delete-reference")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-delete-reference-agent")
	deprecatedID := createTestSkill(t, env, "user_agent", user.ID, agentID, "legacy-deprecated")
	deprecated := skill.SkillStatusDeprecated
	if _, err := env.skillStore.UpdateManagedSkill(t.Context(), skill.ManagedSkillUpdate{
		ID: deprecatedID, UserID: user.ID, AgentID: agentID, Scope: "user_agent",
		Patch: skill.UpdatePatch{Status: &deprecated}, ExpectedDigest: currentSkillDigest(t, env, deprecatedID),
	}); err != nil {
		t.Fatalf("deprecate Skill through managed authority: %v", err)
	}
	rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills/"+deprecatedID+"?scope=user_agent", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("deprecated stable ID status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}

	stableID := createTestSkill(t, env, "user_agent", user.ID, agentID, "delete-by-id")
	rr = doRequestWithSession(t, env.srv, sid, http.MethodDelete, "/api/agents/"+agentID+"/skills/"+stableID+"?scope=user_agent&expected_digest="+currentSkillDigest(t, env, stableID), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("stable ID delete status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}

	replacementID := createTestSkill(t, env, "user_agent", user.ID, agentID, "delete-by-id")
	rr = doRequestWithSession(t, env.srv, sid, http.MethodDelete, "/api/agents/"+agentID+"/skills/delete-by-id?scope=user_agent&expected_digest="+currentSkillDigest(t, env, replacementID), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("active-name delete status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	for _, id := range []string{stableID, replacementID} {
		var count int
		if err := env.db.QueryRow(context.Background(), `SELECT count(*) FROM skill WHERE id = $1`, id).Scan(&count); err != nil {
			t.Fatalf("count deleted skill %s: %v", id, err)
		}
		if count != 0 {
			t.Fatalf("skill %s remains after delete", id)
		}
	}
}

func TestAgentSkillsLifecycleExactScopeFallsBackAfterIDCollision(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-id-name-collision")
	agentID := createAgentAsUser(t, env, sid, "skill-id-name-collision-agent")
	ctx := context.Background()

	if _, err := env.skillStore.CreateManagedSkill(ctx, skill.Skill{
		ID: "deadbeef", Scope: "system_agent", AgentID: agentID,
		Name: "system-collision", Description: "ID occupies the hexadecimal reference",
	}, map[string]string{skill.MainFile: "# System collision\n"}); err != nil {
		t.Fatalf("create colliding ID skill: %v", err)
	}
	wantID := createTestSkill(t, env, "user_agent", user.ID, agentID, "deadbeef")
	rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills/deadbeef?scope=user_agent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("hexadecimal name lookup status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal hexadecimal name response: %v", err)
	}
	if got["id"] != wantID {
		t.Fatalf("hexadecimal name resolved id = %v, want %s", got["id"], wantID)
	}
}

func TestAgentSkillsLifecycleAtomicEditPreservesOrConvertsReflectOwnership(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-edit")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-edit-agent")
	ctx := context.Background()
	created, err := env.skillStore.CreateReflectOwnedUserAgentSkill(ctx, skill.ReflectSkillCreate{
		UserID: user.ID, AgentID: agentID, Name: "task5-reflect-edit", Description: "before", MainFileContent: "before body",
	})
	if err != nil {
		t.Fatalf("create reflect-owned skill: %v", err)
	}
	path := "/api/agents/" + agentID + "/skills/" + created.ID + "?scope=user_agent"
	rr := doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{
		"description": "must not commit", "files": map[string]string{"../escape.md": "invalid"}, "expected_digest": created.ContentDigest,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid file patch status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	assertManagedSkillState(t, env, created.ID, "reflect", "before", "before body")

	// An ordinary edit keeps Reflect ownership while committing metadata and files together.
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{
		"description": "ordinary edit", "files": map[string]string{"SKILL.md": "ordinary body"}, "expected_digest": created.ContentDigest,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("ordinary reflect patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	assertFullSkillMutationResponse(t, rr, created.ID, "reflect")
	assertManagedSkillState(t, env, created.ID, "reflect", "ordinary edit", "ordinary body")
	ordinaryDigest := responseSkillDigest(t, rr)

	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{
		"description": "manual edit", "convert_to_manual": true,
		"files":           map[string]string{"SKILL.md": "manual body", "references/note.md": "note"},
		"expected_digest": ordinaryDigest,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("convert patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	assertFullSkillMutationResponse(t, rr, created.ID, "manual")
	assertManagedSkillState(t, env, created.ID, "manual", "manual edit", "manual body")
	manualDigest := responseSkillDigest(t, rr)

	// Conversion is one-way; a second conversion request is an explicit conflict.
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{"convert_to_manual": true, "expected_digest": manualDigest})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second conversion status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestSkillMutationResponseUsesCommittedSnapshot(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "skill-snapshot-response")
	agentID := createAgentAsUser(t, env, sid, "skill-snapshot-agent")

	rr := doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills", map[string]any{
		"scope": "user_agent", "name": "snapshot-response",
		"files": map[string]string{skill.MainFile: "# Snapshot\n", "references/note.md": "note\n"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	assertFullSkillMutationResponse(t, rr, "", "manual")
	var got map[string]any
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal committed snapshot response: %v", err)
	}
	files, _ := got["files"].([]any)
	if len(files) != 2 || files[0] != skill.MainFile || files[1] != "references/note.md" {
		t.Fatalf("committed snapshot files = %#v, want complete sorted files", files)
	}
	id, _ := got["id"].(string)
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, "/api/agents/"+agentID+"/skills/"+id+"?scope=user_agent", map[string]any{
		"files": map[string]string{"scripts/run.sh": "#!/bin/sh\n"}, "expected_digest": got["content_digest"],
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal updated snapshot response: %v", err)
	}
	files, _ = got["files"].([]any)
	if len(files) != 3 || files[2] != "scripts/run.sh" {
		t.Fatalf("updated committed snapshot files = %#v, want all retained files", files)
	}
}

func assertFullSkillMutationResponse(t *testing.T, rr *httptest.ResponseRecorder, id, createdBy string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal Skill mutation response: %v", err)
	}
	if (id != "" && got["id"] != id) || got["id"] == "" || got["created_by"] != createdBy {
		t.Fatalf("Skill mutation response = %#v, want id=%s created_by=%s", got, id, createdBy)
	}
	if version, ok := got["lifecycle_version"].(float64); !ok || version < 1 {
		t.Fatalf("Skill mutation lifecycle_version = %#v, want positive number", got["lifecycle_version"])
	}
}

func assertManagedSkillState(t *testing.T, env *testEnv, id, createdBy, description, mainFile string) {
	t.Helper()
	identity, err := env.skillStore.GetIdentity(context.Background(), id)
	if err != nil || identity == nil {
		t.Fatalf("skill %s identity = %#v, %v", id, identity, err)
	}
	revision, err := env.skillStore.LoadCurrentRevision(context.Background(), *identity)
	if err != nil {
		t.Fatalf("load managed Skill %s: %v", id, err)
	}
	if revision.Skill.Description != description || skill.CreatedBy(revision.Skill) != createdBy {
		t.Fatalf("skill %s state description=%q created_by=%q", id, revision.Skill.Description, skill.CreatedBy(revision.Skill))
	}
	content := string(revision.Files[skill.MainFile])
	if content != mainFile {
		t.Fatalf("skill %s main file = %q; want %q", id, content, mainFile)
	}
}
