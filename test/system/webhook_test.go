//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testWebhookSyncPersistent drives the capability ingress over real HTTP twice.
// It proves that wait=true returns the model output to an unauthenticated
// caller and that session_mode=persistent resolves the same durable session
// across requests instead of creating two isolated conversations.
func (h *harness) testWebhookSyncPersistent(t *testing.T) {
	fake := newFakeAnthropic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	firstReply := "stored ORBIT " + h.runID
	secondReply := "ORBIT " + h.runID
	fake.enqueueText(firstReply)
	fake.enqueueText(secondReply)

	const modelID = "claude-sonnet-4-6"
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "")
	capabilityPath := h.createWebhookCapability(t, ctx, agentID, "")

	firstBody := `{"event":"persistent_test_1","message":"Remember ORBIT."}`
	secondBody := `{"event":"persistent_test_2","message":"Return the remembered word."}`
	first := h.callWebhookSyncPersistent(t, ctx, capabilityPath, firstBody)
	second := h.callWebhookSyncPersistent(t, ctx, capabilityPath, secondBody)

	if first.Output != firstReply {
		t.Fatalf("first webhook output = %q, want %q", first.Output, firstReply)
	}
	if second.Output != secondReply {
		t.Fatalf("second webhook output = %q, want %q", second.Output, secondReply)
	}
	if first.SessionID == "" || second.SessionID != first.SessionID {
		t.Fatalf("persistent session ids = %q / %q, want the same non-empty id", first.SessionID, second.SessionID)
	}

	reqs := fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("fake received %d model requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		if req.Model != modelID {
			t.Errorf("model request %d = %q, want %q", i+1, req.Model, modelID)
		}
	}

	h.assertPersistentWebhookRows(t, ctx, first.SessionID, agentID, []messageRow{
		{Role: "user", Content: firstBody},
		{Role: "assistant", Content: firstReply},
		{Role: "user", Content: secondBody},
		{Role: "assistant", Content: secondReply},
	})
}

// testGitHubWebhookCompatibility proves GitHub needs no dedicated provider or
// adapter: a GitHub-shaped JSON push delivery reaches the ordinary personal
// webhook capability, which defaults to asynchronous, ephemeral execution.
func (h *harness) testGitHubWebhookCompatibility(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fake.enqueueText("received GitHub push " + h.runID)
	const modelID = "claude-sonnet-4-6"
	providerID := h.createWebhookFakeProvider(t, ctx, fake.baseURL(), "-github")
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID, "-github")
	capabilityPath := h.createWebhookCapability(t, ctx, agentID, "-github")

	payload := `{"ref":"refs/heads/main","repository":{"full_name":"octo-org/hello-world"},"head_commit":{"id":"deadbeef","message":"deploy"}}`
	h.callGitHubWebhook(t, ctx, capabilityPath, payload)

	reqs := fake.waitForRequests(ctx, 1)
	if len(reqs) != 1 {
		t.Fatalf("fake received %d model request(s), want exactly 1", len(reqs))
	}
	if reqs[0].Model != modelID {
		t.Fatalf("model in request = %q, want %q", reqs[0].Model, modelID)
	}
	if !containsMessage(reqs[0].Messages, payload) {
		t.Fatalf("model request did not contain the GitHub payload")
	}
}

func (h *harness) callGitHubWebhook(t *testing.T, ctx context.Context, capabilityPath, payload string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+capabilityPath, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("build GitHub webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-"+h.runID)

	// No cookie jar: the generic capability URL, not a GitHub-specific
	// authentication path, admits the delivery.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("POST GitHub webhook failed: %T\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("POST GitHub webhook = %d, want %d (body: %s)\n%s", resp.StatusCode, http.StatusAccepted, body, h.proc.logTail(40))
	}
}

func containsMessage(messages []string, want string) bool {
	for _, message := range messages {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

func (h *harness) createWebhookFakeProvider(t *testing.T, ctx context.Context, baseURL, suffix string) string {
	t.Helper()
	id := "anthropic-webhook-" + h.runID + suffix
	resp := h.postJSON(t, ctx, "/api/providers", map[string]any{
		"id":       id,
		"type":     "anthropic",
		"name":     id,
		"enabled":  true,
		"api_key":  "system-test-not-a-secret",
		"base_url": baseURL,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST webhook provider = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	return id
}

func (h *harness) createWebhookAgent(t *testing.T, ctx context.Context, model, suffix string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents", map[string]any{
		"name":    "sys-test-webhook-agent-" + h.runID + suffix,
		"model":   model,
		"enabled": true,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST webhook agent = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode webhook agent: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created webhook agent has empty id")
	}
	return created.ID
}

func (h *harness) createWebhookCapability(t *testing.T, ctx context.Context, agentID, suffix string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/webhooks", map[string]any{
		"name":                    "sys-test-webhook-" + h.runID + suffix,
		"agent_id":                agentID,
		"wait_timeout_seconds":    30,
		"max_run_timeout_seconds": 60,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/webhooks = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	parsed, err := url.Parse(created.URL)
	if err != nil || parsed.Path == "" {
		// The one-time URL is a credential; never copy it into test output.
		t.Fatal("created webhook URL has no parseable capability path")
	}
	// Use only the path. The system harness owns a random loopback port, while
	// the configured public base URL may name a different deployment origin.
	return parsed.EscapedPath()
}

type webhookSyncResponse struct {
	SessionID string `json:"session_id"`
	Output    string `json:"output"`
}

func (h *harness) callWebhookSyncPersistent(t *testing.T, ctx context.Context, capabilityPath, body string) webhookSyncResponse {
	t.Helper()
	target := h.baseURL + capabilityPath + "?wait=true&session_mode=persistent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// A client without the harness cookie jar proves the capability is the sole
	// credential. Accidentally routing this endpoint through session auth fails.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		// Transport errors include req.URL, which contains the capability.
		t.Fatalf("POST webhook capability failed: %T\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("POST webhook capability = %d, want %d (body: %s)\n%s", resp.StatusCode, http.StatusOK, payload, h.proc.logTail(40))
	}
	var out webhookSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode synchronous webhook response: %v", err)
	}
	return out
}

type messageRow struct {
	Role    string
	Content string
}

func (h *harness) assertPersistentWebhookRows(t *testing.T, ctx context.Context, sessionID, agentID string, want []messageRow) {
	t.Helper()
	var (
		channel  string
		kind     string
		archived bool
		gotAgent string
		count    int
	)
	if err := h.db.QueryRow(ctx, `
		SELECT channel, kind, archived, coalesce(agent_id, ''), count(*) OVER ()
		FROM ctx_conversation
		WHERE session_id = $1`, sessionID).Scan(&channel, &kind, &archived, &gotAgent, &count); err != nil {
		t.Fatalf("query persistent webhook conversation: %v\n%s", err, h.proc.logTail(40))
	}
	if count != 1 || channel != "webhook" || kind != "chat" || archived || gotAgent != agentID {
		t.Fatalf("persistent conversation = count:%d channel:%q kind:%q archived:%v agent:%q, want 1/webhook/chat/false/%q", count, channel, kind, archived, gotAgent, agentID)
	}

	// Stream completion can precede the final persistence write by a short
	// interval. Poll the durable rows rather than turning scheduler timing into a
	// flaky system-test contract.
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, err := h.db.Query(ctx, `
			SELECT m.role, m.content
			FROM ctx_message m
			JOIN ctx_conversation c ON c.id = m.conversation_id
			WHERE c.session_id = $1
			ORDER BY m.seq`, sessionID)
		if err != nil {
			t.Fatalf("query persistent webhook messages: %v", err)
		}
		got := make([]messageRow, 0, len(want))
		for rows.Next() {
			var row messageRow
			if err := rows.Scan(&row.Role, &row.Content); err != nil {
				rows.Close()
				t.Fatalf("scan persistent webhook message: %v", err)
			}
			got = append(got, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatalf("iterate persistent webhook messages: %v", err)
		}
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("persistent webhook messages = %#v, want %#v", got, want)
		}
		time.Sleep(150 * time.Millisecond)
	}
}
