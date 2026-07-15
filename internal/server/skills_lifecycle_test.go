package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/CherryHQ/stella/internal/skills"
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

	// Explicit pagination must not change the legacy no-parameter full-list contract.
	legacy := listAgentSkillsLifecycle(t, env, sid, agentID, "")
	legacyIDs := skillIDs(legacy.Skills)
	for id := range wantIDs {
		if !legacyIDs[id] {
			t.Fatalf("legacy list omitted created skill %s", id)
		}
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
}

func TestAgentSkillsLifecycleScopeCountsSearchAndReflectFilter(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-filter")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-filter-agent")
	ctx := context.Background()

	createTestSkill(t, env, "user", user.ID, "", "task5-filter-manual-user")
	createTestSkill(t, env, "user_agent", user.ID, agentID, "task5-filter-manual-agent")
	reflectSkill, err := env.pluginHost.SkillStore().CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID: user.ID, AgentID: agentID, Name: "task5-filter-reflect-agent",
		Description: "Needle Description", MainFileContent: "reflect body",
	})
	if err != nil {
		t.Fatalf("create reflect-owned skill: %v", err)
	}

	// Counts apply q/source but ignore the selected scope group.
	got := listAgentSkillsLifecycle(t, env, sid, agentID, "?state=active&scope_group=user&q=needle&created_by=reflect&page_size=12")
	if len(got.Skills) != 0 {
		t.Fatalf("user group unexpectedly returned reflect agent skill: %#v", got.Skills)
	}
	if got.TotalSize != 0 || got.ScopeCounts["all"] != 1 || got.ScopeCounts["agent"] != 1 || got.ScopeCounts["user"] != 0 {
		t.Fatalf("filtered counts = %#v total=%d, want all=1 agent=1 selected total=0", got.ScopeCounts, got.TotalSize)
	}

	agent := listAgentSkillsLifecycle(t, env, sid, agentID, "?state=active&scope_group=agent&q=NEEDLE&created_by=reflect&page_size=12")
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

	stableID := createTestSkill(t, env, "user_agent", user.ID, agentID, "delete-by-id")
	rr := doRequestWithSession(t, env.srv, sid, http.MethodDelete, "/api/agents/"+agentID+"/skills/"+stableID+"?scope=user_agent", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("stable ID delete status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}

	replacementID := createTestSkill(t, env, "user_agent", user.ID, agentID, "delete-by-id")
	rr = doRequestWithSession(t, env.srv, sid, http.MethodDelete, "/api/agents/"+agentID+"/skills/delete-by-id?scope=user_agent", nil)
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

func TestAgentSkillsLifecycleAtomicEditPreservesOrConvertsReflectOwnership(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-edit")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-edit-agent")
	ctx := context.Background()
	created, err := env.pluginHost.SkillStore().CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID: user.ID, AgentID: agentID, Name: "task5-reflect-edit", Description: "before", MainFileContent: "before body",
	})
	if err != nil {
		t.Fatalf("create reflect-owned skill: %v", err)
	}
	path := "/api/agents/" + agentID + "/skills/" + created.ID + "?scope=user_agent"

	// An ordinary edit keeps Reflect ownership while committing metadata and files together.
	rr := doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{
		"description": "ordinary edit", "files": map[string]string{"SKILL.md": "ordinary body"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("ordinary reflect patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	assertManagedSkillState(t, env, created.ID, "reflect", "ordinary edit", "ordinary body")

	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{
		"description": "manual edit", "convert_to_manual": true,
		"files": map[string]string{"SKILL.md": "manual body", "references/note.md": "note"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("convert patch status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	assertManagedSkillState(t, env, created.ID, "manual", "manual edit", "manual body")

	// Conversion is one-way; a second conversion request is an explicit conflict.
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, path, map[string]any{"convert_to_manual": true})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second conversion status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
}

func assertManagedSkillState(t *testing.T, env *testEnv, id, createdBy, description, mainFile string) {
	t.Helper()
	var row struct {
		Description string          `json:"description"`
		Metadata    json.RawMessage `json:"metadata"`
	}
	if err := env.db.QueryRow(context.Background(), `SELECT description, metadata FROM skill WHERE id = $1`, id).Scan(&row.Description, &row.Metadata); err != nil {
		t.Fatalf("read managed skill %s: %v", id, err)
	}
	if row.Description != description || skills.CreatedBy(skills.Skill{Metadata: row.Metadata}) != createdBy {
		t.Fatalf("skill %s state description=%q created_by=%q", id, row.Description, skills.CreatedBy(skills.Skill{Metadata: row.Metadata}))
	}
	content, err := env.pluginHost.SkillStore().LoadFile(context.Background(), id, skills.MainFile)
	if err != nil || content != mainFile {
		t.Fatalf("skill %s main file = %q, %v; want %q", id, content, err, mainFile)
	}
}
