package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestListProfileIdentitiesEmpty(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/users/me/identities", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var wrapper struct {
		Identities []auth.ChannelIdentity `json:"identities"`
	}
	if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	identities := wrapper.Identities
	if len(identities) != 0 {
		t.Errorf("expected 0 identities, got %d", len(identities))
	}
}

func TestListProfileIdentitiesWithLink(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a channel identity for the admin user.
	_, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "12345",
		Name:       "TestAdmin",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/users/me/identities", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var wrapper struct {
		Identities []auth.ChannelIdentity `json:"identities"`
	}
	if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	identities := wrapper.Identities
	if len(identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(identities))
	}
	if identities[0].Platform != "telegram" {
		t.Errorf("platform = %q, want %q", identities[0].Platform, "telegram")
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "testpassword",
		"new_password":     "newpassword123",
	}
	rr := doRequest(t, env, "PATCH", "/api/users/me/password", body)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// Verify the new password works.
	cred, err := env.oidcStore.GetCredentialByUserID(context.Background(), env.adminUser.ID)
	if err != nil {
		t.Fatalf("GetCredentialByUserID: %v", err)
	}
	if err := auth.CheckPassword(cred.PasswordHash, "newpassword123"); err != nil {
		t.Error("new password should work after change")
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "wrongpassword",
		"new_password":     "newpassword123",
	}
	rr := doRequest(t, env, "PATCH", "/api/users/me/password", body)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "testpassword",
		"new_password":     "short",
	}
	rr := doRequest(t, env, "PATCH", "/api/users/me/password", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestGenerateLinkCode(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"platform": "telegram",
	}
	rr := doRequest(t, env, "POST", "/api/users/me/link-code", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var result struct {
		Code     string `json:"code"`
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Code) != 6 {
		t.Errorf("code length = %d, want 6", len(result.Code))
	}
	if result.Platform != "telegram" {
		t.Errorf("platform = %q, want %q", result.Platform, "telegram")
	}
}

func TestGenerateDiscordLinkCode(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "POST", "/api/users/me/link-code", map[string]string{
		"platform": "discord",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var result struct {
		Code     string `json:"code"`
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Code) != 6 || result.Platform != "discord" {
		t.Fatalf("link code response = %#v", result)
	}
}

func TestGenerateLinkCodeInvalidPlatform(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"platform": "invalid",
	}
	rr := doRequest(t, env, "POST", "/api/users/me/link-code", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUnlinkIdentity(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a channel identity.
	identity, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "54321",
		Name:       "TestAdmin",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity: %v", err)
	}

	rr := doRequest(t, env, "DELETE", "/api/users/me/identities/"+identity.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// Verify it's gone.
	identities, err := env.oidcStore.ListChannelIdentitiesByUser(ctx, env.adminUser.ID)
	if err != nil {
		t.Fatalf("ListChannelIdentitiesByUser: %v", err)
	}
	if len(identities) != 0 {
		t.Errorf("expected 0 identities after unlink, got %d", len(identities))
	}
}

func TestUnlinkIdentityOtherUser(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create another user.
	otherUser, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "otheruser", auth.RoleUser)

	// Create a channel identity for the other user.
	identity, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     otherUser.ID,
		Platform:   "qq",
		ExternalID: "99999",
		Name:       "Other",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity: %v", err)
	}

	// Try to unlink it as the admin — should fail (not your identity).
	rr := doRequest(t, env, "DELETE", "/api/users/me/identities/"+identity.ID, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestProfileMemoryAPIWritesFactsBackedProfile(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodPatch, "/api/users/me/memories/"+agentID, map[string]string{
		"content": "Prefers concise answers in Chinese.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	profiles := env.mem.(memory.ProfileStore)
	got, err := profiles.GetProfile(context.Background(), env.adminUser.ID, agentID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if got != "Prefers concise answers in Chinese." {
		t.Fatalf("profile fact = %q, want updated content", got)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/users/me/memories/"+agentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("unmarshal GET memory: %v", err)
	}
	if body.Content != "Prefers concise answers in Chinese." {
		t.Fatalf("GET content = %q, want fact-backed profile", body.Content)
	}
}

func TestProfileSoulAPIWritesFactsBackedSoul(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodPatch, "/api/users/me/soul/"+agentID, map[string]string{
		"soul": "Be crisp and practical.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	profiles := env.mem.(memory.ProfileStore)
	got, err := profiles.GetAgentSoul(context.Background(), env.adminUser.ID, agentID)
	if err != nil {
		t.Fatalf("GetAgentSoul: %v", err)
	}
	if got != "Be crisp and practical." {
		t.Fatalf("soul fact = %q, want updated soul", got)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/users/me/memories/"+agentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var body struct {
		Soul string `json:"soul"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("unmarshal GET memory: %v", err)
	}
	if body.Soul != "Be crisp and practical." {
		t.Fatalf("GET soul = %q, want fact-backed soul", body.Soul)
	}
}

func TestProfileChangelogAPIReadsFactsBackedProfileAndSoul(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/memories/"+agentID, map[string]string{
		"content": "Profile history entry from facts.",
	}); rr.Code != http.StatusOK {
		t.Fatalf("set profile status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/soul/"+agentID, map[string]string{
		"soul": "Soul history entry from facts.",
	}); rr.Code != http.StatusOK {
		t.Fatalf("set soul status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	assertChangelogEntry := func(scope string, wantAfter string) {
		t.Helper()
		rr := doRequest(t, env, http.MethodGet, "/api/users/me/memories/"+agentID+"/changelog?scope="+scope, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s changelog status = %d, want %d (body: %s)", scope, rr.Code, http.StatusOK, rr.Body.String())
		}
		resp := parseResponse(t, rr)
		var body struct {
			Entries []struct {
				Scope     string  `json:"scope"`
				AfterText *string `json:"after_text"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(resp.Data, &body); err != nil {
			t.Fatalf("unmarshal %s changelog: %v", scope, err)
		}
		if len(body.Entries) == 0 {
			t.Fatalf("%s changelog entries = 0, want facts-backed entry", scope)
		}
		if body.Entries[0].Scope != scope {
			t.Fatalf("%s changelog scope = %q, want %q", scope, body.Entries[0].Scope, scope)
		}
		if body.Entries[0].AfterText == nil || *body.Entries[0].AfterText != wantAfter {
			t.Fatalf("%s changelog after_text = %v, want %q", scope, body.Entries[0].AfterText, wantAfter)
		}
	}

	assertChangelogEntry("profile", "Profile history entry from facts.")
	assertChangelogEntry("soul", "Soul history entry from facts.")
}

func TestProfileConstraintAPIWritesManualChangelogSource(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	rr := doRequest(t, env, http.MethodPost, "/api/users/me/memories/"+agentID+"/constraints", map[string]string{
		"text": "Ask before deleting files.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("add constraint status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var body struct {
		Constraints []struct {
			ID string `json:"id"`
		} `json:"constraints"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("unmarshal add constraint response: %v", err)
	}
	if len(body.Constraints) != 1 || body.Constraints[0].ID == "" {
		t.Fatalf("constraints after add = %+v, want one constraint with ID", body.Constraints)
	}

	rr = doRequest(t, env, http.MethodDelete, "/api/users/me/memories/"+agentID+"/constraints/"+body.Constraints[0].ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete constraint status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	q := sqlc.New(env.db)
	logs, err := q.ListMemoryChangelog(context.Background(), sqlc.ListMemoryChangelogParams{
		UserID:  env.adminUser.ID,
		AgentID: agentID,
		Scope:   "constraint",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMemoryChangelog: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("constraint changelog entries = %d, want 2", len(logs))
	}
	for _, log := range logs {
		if log.Source != string(memory.SourceManual) {
			t.Fatalf("constraint changelog action %q source = %q, want %q", log.Action, log.Source, memory.SourceManual)
		}
	}
}

func TestDeleteProfileMemoryResetsFactsAndConstraints(t *testing.T) {
	env := setupAdmin(t)
	agentID := findStellaID(t, env)

	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/memories/"+agentID, map[string]string{
		"content": "Profile to reset.",
	}); rr.Code != http.StatusOK {
		t.Fatalf("set profile status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPatch, "/api/users/me/soul/"+agentID, map[string]string{
		"soul": "Soul to reset.",
	}); rr.Code != http.StatusOK {
		t.Fatalf("set soul status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/users/me/memories/"+agentID+"/constraints", map[string]string{
		"text": "Always be formal.",
	}); rr.Code != http.StatusOK {
		t.Fatalf("add constraint status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	q := sqlc.New(env.db)
	if _, err := memorywrite.CreateFact(context.Background(), env.db, q, memory.FactWrite{
		UserID:  env.adminUser.ID,
		AgentID: agentID,
		Subject: memory.FactSubjectWorld,
		Content: "Knowledge to reset.",
		Source:  memory.SourceManual,
	}); err != nil {
		t.Fatalf("create knowledge fact: %v", err)
	}

	rr := doRequest(t, env, http.MethodDelete, "/api/users/me/memories/"+agentID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	profiles := env.mem.(memory.ProfileStore)
	profile, err := profiles.GetProfile(context.Background(), env.adminUser.ID, agentID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile != "" {
		t.Fatalf("profile after delete = %q, want empty", profile)
	}
	soul, err := profiles.GetAgentSoul(context.Background(), env.adminUser.ID, agentID)
	if err != nil {
		t.Fatalf("GetAgentSoul: %v", err)
	}
	if soul != "" {
		t.Fatalf("soul after delete = %q, want empty", soul)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/users/me/memories/"+agentID+"/constraints", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("constraints status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var body struct {
		Constraints []struct {
			Text string `json:"text"`
		} `json:"constraints"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("unmarshal constraints: %v", err)
	}
	if len(body.Constraints) != 0 {
		t.Fatalf("constraints after delete = %+v, want empty", body.Constraints)
	}

	activeKnowledge, err := q.ListActiveFactsBySubject(context.Background(), sqlc.ListActiveFactsBySubjectParams{
		UserID:  env.adminUser.ID,
		AgentID: agentID,
		Subject: string(memory.FactSubjectWorld),
	})
	if err != nil {
		t.Fatalf("ListActiveFactsBySubject(world): %v", err)
	}
	if len(activeKnowledge) != 0 {
		t.Fatalf("active knowledge facts after delete = %+v, want none", activeKnowledge)
	}
}

func TestProfilePageRoute(t *testing.T) {
	env := setupAdmin(t)

	// /profile is now served by the SPA wildcard; the redirect is handled client-side.
	rr := doRequest(t, env, "GET", "/profile", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "app-root") {
		t.Error("body missing SPA mount point")
	}
}

func TestSettingsAccountPageRoute(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/settings/account", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}

func TestSettingsCredentialsPageRoute(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/settings/credentials", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}
