//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testAgentProviderCredentials proves the process-level credential seam: three
// Agents share one administrator-controlled Provider and model while A and B
// send distinct encrypted key overrides, C falls back to the global key, and a
// live rotation/delete takes effect on A's next turn without a process restart.
func (h *harness) testAgentProviderCredentials(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	providerID := "credential-provider-" + h.runID
	globalKey := "global-key-" + h.runID
	agentAKey := "agent-a-key-" + h.runID
	agentBKey := "agent-b-key-" + h.runID
	rotatedAKey := "agent-a-rotated-key-" + h.runID
	const modelID = "claude-sonnet-4-6"
	h.createFakeProviderNamedWithKey(t, ctx, fake.baseURL(), providerID, globalKey)

	agentA := h.createCredentialAgent(t, ctx, "credential-a-"+h.runID, providerID+"/"+modelID, providerID, agentAKey)
	agentB := h.createCredentialAgent(t, ctx, "credential-b-"+h.runID, providerID+"/"+modelID, providerID, agentBKey)
	agentC := h.createCredentialAgent(t, ctx, "credential-c-"+h.runID, providerID+"/"+modelID, "", "")
	h.assertEncryptedCredentialRows(t, ctx, providerID, map[string]string{agentA: agentAKey, agentB: agentBKey})

	sessionA := h.createSession(t, ctx, agentA)
	sessionB := h.createSession(t, ctx, agentB)
	sessionC := h.createSession(t, ctx, agentC)

	turns := []struct {
		agentID, sessionID, reply, wantKey string
	}{
		{agentA, sessionA, "agent A first", agentAKey},
		{agentB, sessionB, "agent B first", agentBKey},
		{agentC, sessionC, "agent C global", globalKey},
	}
	for _, turn := range turns {
		h.runCredentialTurn(t, ctx, fake, turn.agentID, turn.sessionID, turn.reply, turn.wantKey)
	}

	resp := h.credentialRequest(t, ctx, http.MethodPatch,
		fmt.Sprintf("/api/agents/%s/provider-credentials/%s", agentA, providerID),
		map[string]string{"api_key": rotatedAKey})
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("PATCH Agent credential = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	patchBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read PATCH credential response: %v", err)
	}
	if bytes.Contains(patchBody, []byte(rotatedAKey)) || bytes.Contains(patchBody, []byte(`"api_key"`)) {
		t.Fatal("PATCH credential response exposed write-only key material")
	}
	h.assertEncryptedCredentialRows(t, ctx, providerID, map[string]string{agentA: rotatedAKey, agentB: agentBKey})
	h.runCredentialTurn(t, ctx, fake, agentA, sessionA, "agent A rotated", rotatedAKey)

	resp = h.credentialRequest(t, ctx, http.MethodDelete,
		fmt.Sprintf("/api/agents/%s/provider-credentials/%s", agentA, providerID), nil)
	if resp.StatusCode != http.StatusNoContent {
		drainBody(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("DELETE Agent credential = %d, want %d\n%s", resp.StatusCode, http.StatusNoContent, h.proc.logTail(40))
	}
	_ = resp.Body.Close()
	h.runCredentialTurn(t, ctx, fake, agentA, sessionA, "agent A global", globalKey)

	var model string
	var enabled bool
	if err := h.db.QueryRow(ctx, `SELECT model, enabled FROM agent WHERE id = $1`, agentA).Scan(&model, &enabled); err != nil {
		t.Fatalf("query Agent A after credential delete: %v", err)
	}
	if model != providerID+"/"+modelID || !enabled {
		t.Fatalf("credential delete changed Agent A config: model=%q enabled=%v", model, enabled)
	}
	var providerRows, remainingOverrides int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM provider WHERE id = $1 AND config->>'base_url' = $2`, providerID, fake.baseURL()).Scan(&providerRows); err != nil {
		t.Fatalf("count global Provider rows: %v", err)
	}
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM agent_provider_credential WHERE provider_id = $1`, providerID).Scan(&remainingOverrides); err != nil {
		t.Fatalf("count remaining credential rows: %v", err)
	}
	if providerRows != 1 || remainingOverrides != 1 {
		t.Fatalf("unexpected persistence cardinality: providers=%d overrides=%d", providerRows, remainingOverrides)
	}
}

func (h *harness) createCredentialAgent(t *testing.T, ctx context.Context, name, model, providerID, apiKey string) string {
	t.Helper()
	body := map[string]any{"name": name, "model": model, "enabled": true}
	if providerID != "" {
		body["provider_credentials"] = []map[string]string{{"provider_id": providerID, "api_key": apiKey}}
	}
	resp := h.postJSON(t, ctx, "/api/agents", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST credential Agent = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("decode credential Agent: id=%q err=%v", created.ID, err)
	}
	return created.ID
}

func (h *harness) runCredentialTurn(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID, sessionID, reply, wantKey string) {
	t.Helper()
	fake.enqueueText(reply)
	before := len(fake.requests())
	_, got := h.streamChatTurn(t, ctx, agentID, sessionID, "credential probe "+reply)
	if got != reply {
		t.Fatalf("credential turn reply = %q, want %q", got, reply)
	}
	requests := fake.requests()
	if len(requests) != before+1 {
		t.Fatalf("credential turn produced %d model requests, want 1", len(requests)-before)
	}
	if requests[before].APIKey != wantKey {
		t.Fatalf("credential turn used wrong x-api-key for Agent %s", agentID)
	}
}

func (h *harness) assertEncryptedCredentialRows(t *testing.T, ctx context.Context, providerID string, agents map[string]string) {
	t.Helper()
	rows, err := h.db.Query(ctx,
		`SELECT agent_id, api_key_enc FROM agent_provider_credential WHERE provider_id = $1 ORDER BY agent_id`, providerID)
	if err != nil {
		t.Fatalf("query encrypted credential rows: %v", err)
	}
	defer rows.Close()
	seen := make(map[string]bool, len(agents))
	for rows.Next() {
		var agentID, ciphertext string
		if err := rows.Scan(&agentID, &ciphertext); err != nil {
			t.Fatalf("scan encrypted credential row: %v", err)
		}
		plaintext, ok := agents[agentID]
		if !ok {
			t.Fatalf("unexpected credential row for Agent %s", agentID)
		}
		if ciphertext == "" || strings.Contains(ciphertext, plaintext) {
			t.Fatalf("Agent %s credential is not encrypted at rest", agentID)
		}
		seen[agentID] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate encrypted credential rows: %v", err)
	}
	if len(seen) != len(agents) {
		t.Fatalf("encrypted credential row count = %d, want %d", len(seen), len(agents))
	}
}

func (h *harness) credentialRequest(t *testing.T, ctx context.Context, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal credential request: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		t.Fatalf("build credential request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", method, path, err, h.proc.logTail(40))
	}
	return resp
}
