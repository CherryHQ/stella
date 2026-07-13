package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type knowledgeAPIItem struct {
	ID              string     `json:"id"`
	Content         string     `json:"content"`
	Source          string     `json:"source"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	RemovalSource   *string    `json:"removal_source"`
	DeprecatedAt    *time.Time `json:"deprecated_at"`
	RestoreDeadline *time.Time `json:"restore_deadline"`
	IsRestorable    bool       `json:"is_restorable"`
}

type knowledgeAPIList struct {
	Knowledge     []knowledgeAPIItem `json:"knowledge"`
	TotalSize     int                `json:"total_size"`
	NextPageToken *string            `json:"next_page_token"`
}

func TestKnowledgeAPIActiveCreateEditDeleteRemovedRestoreFlow(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	created := createKnowledgeAPI(t, env, agentID, "  remembers green tea  ")
	if created.Content != "remembers green tea" || created.Source != "manual" || created.IsRestorable {
		t.Fatalf("created knowledge = %+v, want trimmed manual active item", created)
	}

	active := listKnowledgeAPI(t, env, agentID, "active", 20, "")
	if active.TotalSize != 1 || len(active.Knowledge) != 1 || active.Knowledge[0].ID != created.ID {
		t.Fatalf("active knowledge = %+v, want created item", active)
	}

	rr := doRequest(t, env, http.MethodPatch, knowledgeItemPath(agentID, created.ID), map[string]string{
		"content": "  now prefers oolong  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("edit status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	edited := decodeKnowledgeItem(t, rr)
	if edited.ID == created.ID || edited.Content != "now prefers oolong" || edited.Source != "manual" {
		t.Fatalf("edited knowledge = %+v, want manual replacement with a new ID", edited)
	}

	rr = doRequest(t, env, http.MethodDelete, knowledgeItemPath(agentID, edited.ID), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	removed := listKnowledgeAPI(t, env, agentID, "removed", 20, "")
	if removed.TotalSize != 1 || len(removed.Knowledge) != 1 {
		t.Fatalf("removed knowledge = %+v, want one manual deprecation", removed)
	}
	item := removed.Knowledge[0]
	if item.ID != edited.ID || item.RemovalSource == nil || *item.RemovalSource != "manual" || item.DeprecatedAt == nil || item.RestoreDeadline == nil || !item.IsRestorable {
		t.Fatalf("removed item = %+v, want explicit restorable lifecycle fields", item)
	}
	if item.RestoreDeadline.Sub(*item.DeprecatedAt) != memorywrite.KnowledgeRestoreWindow {
		t.Fatalf("restore window = %s, want %s", item.RestoreDeadline.Sub(*item.DeprecatedAt), memorywrite.KnowledgeRestoreWindow)
	}

	rr = doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, edited.ID)+"/restore", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	restored := decodeKnowledgeItem(t, rr)
	if restored.ID != edited.ID || restored.Content != edited.Content || restored.IsRestorable {
		t.Fatalf("restored knowledge = %+v, want active item restored in place", restored)
	}

	// Restore is server-authoritative and idempotent for an already-active owned fact.
	rr = doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, edited.ID)+"/restore", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("idempotent restore status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestKnowledgeAPIFailsClosedForAuthenticationAgentAndRecordOwnership(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	agentID := findStellaID(t, env)
	adminFact := createKnowledgeAPI(t, env, agentID, "admin-owned fact")

	rr := doUnauthRequest(t, env.srv, http.MethodGet, knowledgeCollectionPath(agentID), nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	_, ordinaryToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-user", auth.RoleUser)
	restrictedID := createTestAgent(t, env, config.Agent{
		Name: "Knowledge Restricted", Model: "anthropic/claude-sonnet-4-6", Scope: config.AgentScopeRestricted, Enabled: true,
	})
	rr = doRequestWithSession(t, env.srv, ordinaryToken, http.MethodGet, knowledgeCollectionPath(restrictedID), nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("inaccessible agent status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	otherUser, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-owner", auth.RoleUser)
	otherFact, err := memorywrite.CreateKnowledge(ctx, env.db, sqlc.New(env.db), memorywrite.KnowledgeCreateInput{
		UserID: otherUser.ID, AgentID: agentID, Content: "other-user fact",
	})
	if err != nil {
		t.Fatalf("seed other-user knowledge: %v", err)
	}
	assertKnowledgeRecordNotFound(t, env, http.MethodPatch, knowledgeItemPath(agentID, otherFact.ID), map[string]string{"content": "stolen"})

	otherAgentFact, err := memorywrite.CreateKnowledge(ctx, env.db, sqlc.New(env.db), memorywrite.KnowledgeCreateInput{
		UserID: env.adminUser.ID, AgentID: restrictedID, Content: "other-agent fact",
	})
	if err != nil {
		t.Fatalf("seed other-agent knowledge: %v", err)
	}
	assertKnowledgeRecordNotFound(t, env, http.MethodDelete, knowledgeItemPath(agentID, otherAgentFact.ID), nil)
	assertKnowledgeRecordNotFound(t, env, http.MethodPost, knowledgeItemPath(agentID, "not-a-fact")+"/restore", nil)

	// The owned fact remains accessible after all foreign-ID probes.
	active := listKnowledgeAPI(t, env, agentID, "active", 20, "")
	if len(active.Knowledge) != 1 || active.Knowledge[0].ID != adminFact.ID {
		t.Fatalf("active knowledge after foreign probes = %+v, want admin fact unchanged", active)
	}
}

func TestKnowledgeAPIRejectsInvalidPaginationStateAndTokens(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)
	createKnowledgeAPI(t, env, agentID, "first")
	createKnowledgeAPI(t, env, agentID, "second")

	for _, path := range []string{
		knowledgeCollectionPath(agentID) + "?state=unknown",
		knowledgeCollectionPath(agentID) + "?page_size=0",
		knowledgeCollectionPath(agentID) + "?page_size=101",
		knowledgeCollectionPath(agentID) + "?page_token=not-base64",
	} {
		rr := doRequest(t, env, http.MethodGet, path, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d (body: %s)", path, rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	}

	first := listKnowledgeAPI(t, env, agentID, "active", 1, "")
	if first.NextPageToken == nil || *first.NextPageToken == "" {
		t.Fatal("first active page omitted next_page_token")
	}
	wrongKind := mutateOpaqueToken(t, *first.NextPageToken, "kind", "changelog")
	wrongState := mutateOpaqueToken(t, *first.NextPageToken, "state", "removed")
	for _, token := range []string{wrongKind, wrongState} {
		rr := doRequest(t, env, http.MethodGet, knowledgeCollectionPath(agentID)+"?state=active&page_token="+token, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("wrong token status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	}
}

func TestKnowledgeAPIKeysetPagesRemainContinuousAcrossInterveningWrites(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		env := setupAdmin(t)
		agentID := findStellaID(t, env)
		original := map[string]bool{}
		for _, content := range []string{"one", "two", "three"} {
			original[createKnowledgeAPI(t, env, agentID, content).ID] = true
		}

		first := listKnowledgeAPI(t, env, agentID, "active", 2, "")
		if first.NextPageToken == nil {
			t.Fatal("first active page omitted token")
		}
		intervening := createKnowledgeAPI(t, env, agentID, "newer write")
		second := listKnowledgeAPI(t, env, agentID, "active", 2, *first.NextPageToken)
		assertStableOriginalPageSet(t, original, first.Knowledge, second.Knowledge, intervening.ID)
	})

	t.Run("removed", func(t *testing.T) {
		env := setupAdmin(t)
		agentID := findStellaID(t, env)
		original := map[string]bool{}
		for _, content := range []string{"one", "two", "three"} {
			item := createKnowledgeAPI(t, env, agentID, content)
			deleteKnowledgeAPI(t, env, agentID, item.ID)
			original[item.ID] = true
		}

		first := listKnowledgeAPI(t, env, agentID, "removed", 2, "")
		if first.NextPageToken == nil {
			t.Fatal("first removed page omitted token")
		}
		intervening := createKnowledgeAPI(t, env, agentID, "newer removal")
		deleteKnowledgeAPI(t, env, agentID, intervening.ID)
		second := listKnowledgeAPI(t, env, agentID, "removed", 2, *first.NextPageToken)
		assertStableOriginalPageSet(t, original, first.Knowledge, second.Knowledge, intervening.ID)
	})
}

func TestKnowledgeAPIRestoreConflictExpiredAndReplacementStates(t *testing.T) {
	t.Run("trimmed duplicate is conflict", func(t *testing.T) {
		env := setupAdmin(t)
		agentID := findStellaID(t, env)
		removed := createKnowledgeAPI(t, env, agentID, "same content")
		deleteKnowledgeAPI(t, env, agentID, removed.ID)
		createKnowledgeAPI(t, env, agentID, "  same content  ")

		rr := doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, removed.ID)+"/restore", nil)
		if rr.Code != http.StatusConflict {
			t.Fatalf("duplicate restore status = %d, want %d (body: %s)", rr.Code, http.StatusConflict, rr.Body.String())
		}
	})

	t.Run("exact deadline is gone", func(t *testing.T) {
		env := setupAdmin(t)
		agentID := findStellaID(t, env)
		removed := createKnowledgeAPI(t, env, agentID, "expired content")
		deleteKnowledgeAPI(t, env, agentID, removed.ID)
		deadline := time.Now().UTC().Add(-memorywrite.KnowledgeRestoreWindow)
		if _, err := env.db.Exec(context.Background(), `
			UPDATE ctx_agent_memory_changelog
			SET created_at = $1
			WHERE user_id = $2 AND agent_id = $3 AND entity_id = $4 AND action = 'deprecate'`,
			deadline, env.adminUser.ID, agentID, removed.ID); err != nil {
			t.Fatalf("age deprecation to exact deadline: %v", err)
		}

		rr := doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, removed.ID)+"/restore", nil)
		if rr.Code != http.StatusGone {
			t.Fatalf("expired restore status = %d, want %d (body: %s)", rr.Code, http.StatusGone, rr.Body.String())
		}
	})

	t.Run("replacement is not restorable", func(t *testing.T) {
		env := setupAdmin(t)
		agentID := findStellaID(t, env)
		original := createKnowledgeAPI(t, env, agentID, "old content")
		rr := doRequest(t, env, http.MethodPatch, knowledgeItemPath(agentID, original.ID), map[string]string{"content": "new content"})
		if rr.Code != http.StatusOK {
			t.Fatalf("replace status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
		}

		removed := listKnowledgeAPI(t, env, agentID, "removed", 20, "")
		if removed.TotalSize != 0 || len(removed.Knowledge) != 0 {
			t.Fatalf("replacement appeared in removed list: %+v", removed)
		}
		rr = doRequest(t, env, http.MethodPost, knowledgeItemPath(agentID, original.ID)+"/restore", nil)
		if rr.Code != http.StatusConflict {
			t.Fatalf("replacement restore status = %d, want %d (body: %s)", rr.Code, http.StatusConflict, rr.Body.String())
		}
	})
}

func createKnowledgeAPI(t *testing.T, env *testEnv, agentID string, content string) knowledgeAPIItem {
	t.Helper()
	rr := doRequest(t, env, http.MethodPost, knowledgeCollectionPath(agentID), map[string]string{"content": content})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create knowledge status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	return decodeKnowledgeItem(t, rr)
}

func deleteKnowledgeAPI(t *testing.T, env *testEnv, agentID string, factID string) {
	t.Helper()
	rr := doRequest(t, env, http.MethodDelete, knowledgeItemPath(agentID, factID), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete knowledge status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func listKnowledgeAPI(t *testing.T, env *testEnv, agentID string, state string, pageSize int, pageToken string) knowledgeAPIList {
	t.Helper()
	path := knowledgeCollectionPath(agentID) + "?state=" + state + "&page_size=" + jsonNumber(pageSize)
	if pageToken != "" {
		path += "&page_token=" + pageToken
	}
	rr := doRequest(t, env, http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list %s knowledge status = %d, want %d (body: %s)", state, rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var result knowledgeAPIList
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("decode %s knowledge list: %v", state, err)
	}
	return result
}

func decodeKnowledgeItem(t *testing.T, rr *httptest.ResponseRecorder) knowledgeAPIItem {
	t.Helper()
	resp := parseResponse(t, rr)
	var item knowledgeAPIItem
	if err := json.Unmarshal(resp.Data, &item); err != nil {
		t.Fatalf("decode knowledge item: %v", err)
	}
	return item
}

func knowledgeCollectionPath(agentID string) string {
	return "/api/users/me/memories/" + agentID + "/knowledge"
}

func knowledgeItemPath(agentID string, factID string) string {
	return knowledgeCollectionPath(agentID) + "/" + factID
}

func assertKnowledgeRecordNotFound(t *testing.T, env *testEnv, method string, path string, body any) {
	t.Helper()
	rr := doRequest(t, env, method, path, body)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("%s %s status = %d, want %d (body: %s)", method, path, rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

func mutateOpaqueToken(t *testing.T, token string, key string, value string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decode valid opaque token: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode opaque token JSON: %v", err)
	}
	payload[key] = value
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode mutated token JSON: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func assertStableOriginalPageSet(t *testing.T, original map[string]bool, first []knowledgeAPIItem, second []knowledgeAPIItem, interveningID string) {
	t.Helper()
	seen := make(map[string]bool, len(original))
	for _, page := range [][]knowledgeAPIItem{first, second} {
		for _, item := range page {
			if item.ID == interveningID {
				t.Fatalf("intervening item %s leaked into continuation page", item.ID)
			}
			if seen[item.ID] {
				t.Fatalf("item %s appeared on both keyset pages", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != len(original) {
		t.Fatalf("paged original IDs = %v, want %v", seen, original)
	}
	for id := range original {
		if !seen[id] {
			t.Fatalf("original item %s was skipped across keyset pages", id)
		}
	}
}

func jsonNumber(value int) string {
	return strconv.Itoa(value)
}
