package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

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
		"?state=removed&scope=user_agent&page_size=12&page_token=" + url.QueryEscape(*first.NextPageToken),
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

func TestAgentSkillsLifecycleRemovedFiltersCountsAndAdminBoundary(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-removed-filters")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-removed-filters-agent")
	ctx := context.Background()
	store := env.pluginHost.SkillStore()

	userSkillID := createTestSkill(t, env, "user", user.ID, "", "removed-needle-user")
	agentSkillID := createTestSkill(t, env, "user_agent", user.ID, agentID, "removed-needle-agent")
	for _, item := range []struct {
		id, scope, owner, agent string
	}{
		{id: userSkillID, scope: "user", owner: user.ID},
		{id: agentSkillID, scope: "user_agent", owner: user.ID, agent: agentID},
	} {
		if _, err := store.DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
			ID: item.id, UserID: item.owner, AgentID: item.agent, Scope: item.scope, DeprecatedBy: user.ID,
		}); err != nil {
			t.Fatalf("deprecate %s: %v", item.id, err)
		}
	}

	userPage := listAgentSkillsLifecycle(t, env, sid, agentID, "?state=removed&scope_group=user&q=REMOVED-NEEDLE&created_by=manual&page_size=12")
	if len(userPage.Skills) != 1 || userPage.Skills[0]["id"] != userSkillID || userPage.TotalSize != 1 {
		t.Fatalf("removed user facet = %#v", userPage)
	}
	if userPage.ScopeCounts["all"] != 2 || userPage.ScopeCounts["user"] != 1 || userPage.ScopeCounts["agent"] != 1 {
		t.Fatalf("removed user scope counts = %#v", userPage.ScopeCounts)
	}

	systemAgentID := createTestSkill(t, env, "system_agent", "", agentID, "removed-admin-only")
	if _, err := store.DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
		ID: systemAgentID, AgentID: agentID, Scope: "system_agent", DeprecatedBy: env.adminUser.ID,
	}); err != nil {
		t.Fatalf("deprecate system-agent skill: %v", err)
	}
	ordinary := listAgentSkillsLifecycle(t, env, sid, agentID, "?state=removed&scope_group=agent&q=ADMIN-ONLY&created_by=manual&page_size=12")
	if ordinary.TotalSize != 0 || ordinary.ScopeCounts["all"] != 0 {
		t.Fatalf("ordinary user saw admin removed skill: %#v", ordinary)
	}
	admin := listAgentSkillsLifecycle(t, env, env.bearerToken, agentID, "?state=removed&scope_group=agent&q=ADMIN-ONLY&created_by=manual&page_size=12")
	if len(admin.Skills) != 1 || admin.Skills[0]["id"] != systemAgentID || admin.TotalSize != 1 || admin.ScopeCounts["agent"] != 1 {
		t.Fatalf("admin removed facet = %#v", admin)
	}
}

func TestAgentSkillsLifecycleRemovedStableIDDetailFilesAndRestore(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-removed")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-removed-agent")
	id := createTestSkill(t, env, "user_agent", user.ID, agentID, "task5-removed")
	ctx := context.Background()

	if _, err := env.pluginHost.SkillStore().DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
		ID: id, UserID: user.ID, AgentID: agentID, Scope: "user_agent", DeprecatedBy: user.ID,
	}); err != nil {
		t.Fatalf("deprecate managed skill: %v", err)
	}

	removed := listAgentSkillsLifecycle(t, env, sid, agentID, "?state=removed&scope=user_agent&q=TASK5-REMOVED&page_size=12")
	if len(removed.Skills) != 1 {
		t.Fatalf("removed list = %#v, want one retained skill", removed)
	}
	item := removed.Skills[0]
	if item["id"] != id || item["status"] != "deprecated" || item["removal_source"] != "manual" || item["is_restorable"] != true {
		t.Fatalf("removed item = %#v", item)
	}
	if item["deprecated_at"] == nil || item["restore_deadline"] == nil {
		t.Fatalf("removed lifecycle timestamps missing: %#v", item)
	}

	detailPath := "/api/agents/" + agentID + "/skills/" + id + "?scope=user_agent"
	rr := doRequestWithSession(t, env.srv, sid, http.MethodGet, detailPath, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("removed detail status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills/"+id+"/file?scope=user_agent&path=reference.md", nil)
	if rr.Code != http.StatusOK || !json.Valid(rr.Body.Bytes()) || !containsResponseData(rr.Body.Bytes(), "reference content") {
		t.Fatalf("removed file status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// Stable IDs must still fail closed when the exact scope does not match.
	rr = doRequestWithSession(t, env.srv, sid, http.MethodGet, "/api/agents/"+agentID+"/skills/"+id+"?scope=user", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-scope detail status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, http.MethodPatch, detailPath, map[string]any{"description": "must not edit"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("deprecated patch status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+id+"/restore?scope=user_agent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	// Repeating restore for the same owned row is idempotent.
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+id+"/restore?scope=user_agent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("idempotent restore status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkillsLifecycleRestoreMapsConflictExpiryAndNonQualifying(t *testing.T) {
	env := setupAdmin(t)
	user, sid := newNonAdmin(t, env, "skill-lifecycle-restore-errors")
	agentID := createAgentAsUser(t, env, sid, "skill-lifecycle-restore-errors-agent")
	ctx := context.Background()

	deprecate := func(name string) string {
		t.Helper()
		id := createTestSkill(t, env, "user_agent", user.ID, agentID, name)
		if _, err := env.pluginHost.SkillStore().DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
			ID: id, UserID: user.ID, AgentID: agentID, Scope: "user_agent", DeprecatedBy: user.ID,
		}); err != nil {
			t.Fatalf("deprecate %s: %v", name, err)
		}
		return id
	}

	conflictID := deprecate("task5-restore-conflict")
	createTestSkill(t, env, "user_agent", user.ID, agentID, "task5-restore-conflict")
	rr := doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+conflictID+"/restore?scope=user_agent", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate restore status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	expiredID := deprecate("task5-restore-expired")
	if _, err := env.db.Exec(ctx, `UPDATE skill_changelog SET created_at = $1 WHERE skill_id = $2 AND action = 'deprecate'`, time.Now().UTC().Add(-2160*time.Hour), expiredID); err != nil {
		t.Fatalf("expire deprecation: %v", err)
	}
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+expiredID+"/restore?scope=user_agent", nil)
	if rr.Code != http.StatusGone {
		t.Fatalf("expired restore status = %d, want 410 (body: %s)", rr.Code, rr.Body.String())
	}

	nonQualifyingID := deprecate("task5-restore-nonqualifying")
	if _, err := env.db.Exec(ctx, `UPDATE skill_changelog SET metadata = '{"reason":"replacement"}'::jsonb WHERE skill_id = $1 AND action = 'deprecate'`, nonQualifyingID); err != nil {
		t.Fatalf("replace deprecation metadata: %v", err)
	}
	rr = doRequestWithSession(t, env.srv, sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+nonQualifyingID+"/restore?scope=user_agent", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("non-qualifying restore status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkillsLifecycleRestoreEnforcesOwnerScopeAndAdminGate(t *testing.T) {
	env := setupAdmin(t)
	owner, ownerSID := newNonAdmin(t, env, "skill-lifecycle-restore-owner")
	other, otherSID := newNonAdmin(t, env, "skill-lifecycle-restore-other")
	agentID := createAgentAsUser(t, env, ownerSID, "skill-lifecycle-restore-auth-agent")
	if err := env.authStore.AssignAgent(context.Background(), other.ID, agentID); err != nil {
		t.Fatalf("assign other user to agent: %v", err)
	}

	ctx := context.Background()
	userAgentID := createTestSkill(t, env, "user_agent", owner.ID, agentID, "task5-restore-auth-user")
	if _, err := env.pluginHost.SkillStore().DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
		ID: userAgentID, UserID: owner.ID, AgentID: agentID, Scope: "user_agent", DeprecatedBy: owner.ID,
	}); err != nil {
		t.Fatalf("deprecate user-agent skill: %v", err)
	}

	for _, tc := range []struct {
		name, sid, scope string
	}{
		{name: "cross user", sid: otherSID, scope: "user_agent"},
		{name: "cross scope", sid: ownerSID, scope: "user"},
		{name: "cross admin scope", sid: ownerSID, scope: "system_agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, tc.sid, http.MethodPost, "/api/agents/"+agentID+"/skills/"+userAgentID+"/restore?scope="+tc.scope, nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("restore status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
			}
		})
	}

	systemAgentID := createTestSkill(t, env, "system_agent", "", agentID, "task5-restore-auth-system")
	if _, err := env.pluginHost.SkillStore().DeprecateManagedSkill(ctx, skills.ManagedSkillDeprecate{
		ID: systemAgentID, AgentID: agentID, Scope: "system_agent", DeprecatedBy: env.adminUser.ID,
	}); err != nil {
		t.Fatalf("deprecate system-agent skill: %v", err)
	}
	for _, path := range []string{
		"/api/agents/" + agentID + "/skills/" + systemAgentID + "?scope=system_agent",
		"/api/agents/" + agentID + "/skills/" + systemAgentID + "/file?scope=system_agent&path=reference.md",
	} {
		rr := doRequestWithSession(t, env.srv, ownerSID, http.MethodGet, path, nil)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("non-admin removed system-agent read status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
		}
	}
	rr := doRequestWithSession(t, env.srv, ownerSID, http.MethodPost, "/api/agents/"+agentID+"/skills/"+systemAgentID+"/restore?scope=system_agent", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin system-agent restore status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, env.bearerToken, http.MethodPost, "/api/agents/"+agentID+"/skills/"+systemAgentID+"/restore?scope=system_agent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin system-agent restore status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
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

func containsResponseData(body []byte, value string) bool {
	// Success responses in this server are the resource itself, not an envelope.
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return false
	}
	return data["content"] == value
}
