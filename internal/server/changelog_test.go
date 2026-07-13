package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type changelogAPIEntry struct {
	ID         string    `json:"id"`
	Scope      string    `json:"scope"`
	Action     string    `json:"action"`
	BeforeText *string   `json:"before_text"`
	AfterText  *string   `json:"after_text"`
	CreatedAt  time.Time `json:"created_at"`
}

type changelogAPIList struct {
	Entries       []changelogAPIEntry `json:"entries"`
	NextPageToken *string             `json:"next_page_token"`
}

func TestProfileChangelogProjectsLogicalKnowledgeActions(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	created := createKnowledgeAPI(t, env, agentID, "old knowledge")
	rr := doRequest(t, env, http.MethodPatch, knowledgeItemPath(agentID, created.ID), map[string]string{"content": "new knowledge"})
	if rr.Code != http.StatusOK {
		t.Fatalf("edit knowledge status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	edited := decodeKnowledgeItem(t, rr)
	deleteKnowledgeAPI(t, env, agentID, edited.ID)
	rr = doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, edited.ID)+"/restore", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore knowledge status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	curator, err := memorywrite.CreateFact(context.Background(), env.db, sqlc.New(env.db), memory.FactWrite{
		UserID: env.adminUser.ID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "curator knowledge", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create curator knowledge: %v", err)
	}
	if _, err := memorywrite.ApplyFactBatch(context.Background(), env.db, sqlc.New(env.db), env.adminUser.ID, agentID, []memorywrite.FactBatchOperation{{
		Action: memorywrite.FactBatchDeprecateMany, Subject: memory.FactSubjectWorld, TargetFactIDs: []string{curator.ID},
		Metadata: json.RawMessage(`{"curator":"usage"}`),
	}}); err != nil {
		t.Fatalf("curator deprecate knowledge: %v", err)
	}

	page, raw := listChangelogAPI(t, env, agentID, "knowledge", 20, "")
	if _, exists := raw["total_size"]; exists {
		t.Fatal("changelog response must not expose total_size")
	}
	actions := make(map[string]int)
	for _, entry := range page.Entries {
		if entry.Scope != "knowledge" {
			t.Fatalf("knowledge changelog scope = %q, want knowledge", entry.Scope)
		}
		actions[entry.Action]++
		if entry.Action == "edit" {
			if entry.BeforeText == nil || *entry.BeforeText != "old knowledge" || entry.AfterText == nil || *entry.AfterText != "new knowledge" {
				t.Fatalf("edit diff = before:%v after:%v, want old/new", entry.BeforeText, entry.AfterText)
			}
		}
	}
	for _, action := range []string{"create", "edit", "manual_delete", "curator_remove", "restore"} {
		if actions[action] == 0 {
			t.Errorf("logical knowledge changelog omitted %q: %+v", action, page.Entries)
		}
	}
	if actions["edit"] != 1 {
		t.Fatalf("edit logical entries = %d, want one coalesced replacement", actions["edit"])
	}
}

func TestProfileChangelogMergedKeysetHasNoGapOrDuplicateAtEqualTimestamps(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/memories/"+agentID, map[string]string{"content": "profile entry"}); rr.Code != http.StatusOK {
		t.Fatalf("set profile status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/soul/"+agentID, map[string]string{"soul": "soul entry"}); rr.Code != http.StatusOK {
		t.Fatalf("set soul status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/users/me/memories/"+agentID+"/constraints", map[string]string{"text": "constraint entry"}); rr.Code != http.StatusOK {
		t.Fatalf("add constraint status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	createKnowledgeAPI(t, env, agentID, "knowledge entry")

	// Equal timestamps force the global merge to use ID as the stable tie-breaker.
	equalTime := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	if _, err := env.db.Exec(context.Background(), `
		UPDATE ctx_agent_memory_changelog SET created_at = $1
		WHERE user_id = $2 AND agent_id = $3`, equalTime, env.adminUser.ID, agentID); err != nil {
		t.Fatalf("align changelog timestamps: %v", err)
	}

	first, _ := listChangelogAPI(t, env, agentID, "", 2, "")
	if len(first.Entries) != 2 || first.NextPageToken == nil {
		t.Fatalf("first merged page = %+v, want two entries and token", first)
	}
	intervening := createKnowledgeAPI(t, env, agentID, "intervening knowledge")
	second, _ := listChangelogAPI(t, env, agentID, "", 2, *first.NextPageToken)
	if len(second.Entries) != 2 {
		t.Fatalf("second merged page entries = %d, want 2: %+v", len(second.Entries), second.Entries)
	}

	seenIDs := map[string]bool{}
	seenScopes := map[string]bool{}
	for _, entry := range append(first.Entries, second.Entries...) {
		if seenIDs[entry.ID] {
			t.Fatalf("merged cursor duplicated changelog ID %s", entry.ID)
		}
		seenIDs[entry.ID] = true
		seenScopes[entry.Scope] = true
		if entry.AfterText != nil && *entry.AfterText == intervening.Content {
			t.Fatalf("intervening write leaked behind existing cursor: %+v", entry)
		}
	}
	wantScopes := []string{"constraint", "knowledge", "profile", "soul"}
	sort.Strings(wantScopes)
	for _, scope := range wantScopes {
		if !seenScopes[scope] {
			t.Fatalf("merged pages omitted %s scope: %+v", scope, seenScopes)
		}
	}
}

func TestProfileChangelogRejectsInvalidScopePageAndToken(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	for _, path := range []string{
		changelogPath(agentID) + "?scope=fact",
		changelogPath(agentID) + "?page_size=0",
		changelogPath(agentID) + "?page_size=101",
		changelogPath(agentID) + "?page_token=not-base64",
	} {
		rr := doRequest(t, env, http.MethodGet, path, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d (body: %s)", path, rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	}

	createKnowledgeAPI(t, env, agentID, "token one")
	createKnowledgeAPI(t, env, agentID, "token two")
	knowledgePage := listKnowledgeAPI(t, env, agentID, "active", 1, "")
	if knowledgePage.NextPageToken == nil {
		t.Fatal("knowledge page omitted token")
	}
	rr := doRequest(t, env, http.MethodGet, changelogPath(agentID)+"?page_token="+*knowledgePage.NextPageToken, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong-kind changelog token status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	for _, content := range []string{"profile one", "profile two"} {
		rr = doRequest(t, env, http.MethodPatch, "/api/users/me/memories/"+agentID, map[string]string{"content": content})
		if rr.Code != http.StatusOK {
			t.Fatalf("set profile %q status = %d (body: %s)", content, rr.Code, rr.Body.String())
		}
	}
	profilePage, _ := listChangelogAPI(t, env, agentID, "profile", 1, "")
	if profilePage.NextPageToken == nil {
		t.Fatal("profile changelog page omitted token")
	}
	rr = doRequest(t, env, http.MethodGet, changelogPath(agentID)+"?scope=soul&page_token="+*profilePage.NextPageToken, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong-scope changelog token status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestProfileChangelogRequiresAccessibleAgent(t *testing.T) {
	env := setupAdmin(t)
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "changelog-user", auth.RoleUser)
	restrictedID := createTestAgent(t, env, config.Agent{
		Name: "Changelog Restricted", Model: "anthropic/claude-sonnet-4-6", Scope: config.AgentScopeRestricted, Enabled: true,
	})
	rr := doRequestWithSession(t, env.srv, token, http.MethodGet, changelogPath(restrictedID), nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("inaccessible changelog status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func listChangelogAPI(t *testing.T, env *testEnv, agentID string, scope string, pageSize int, pageToken string) (changelogAPIList, map[string]json.RawMessage) {
	t.Helper()
	path := changelogPath(agentID) + "?page_size=" + jsonNumber(pageSize)
	if scope != "" {
		path += "&scope=" + scope
	}
	if pageToken != "" {
		path += "&page_token=" + pageToken
	}
	rr := doRequest(t, env, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list changelog status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var page changelogAPIList
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		t.Fatalf("decode changelog page: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		t.Fatalf("decode raw changelog page: %v", err)
	}
	return page, raw
}

func changelogPath(agentID string) string {
	return "/api/users/me/memories/" + agentID + "/changelog"
}
