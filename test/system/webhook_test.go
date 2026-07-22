//go:build system

package system

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
)

// testGitHubWebhookIngress proves the root-mounted capability ingress through
// the real process: admin configuration, vault-backed GitHub secret issuance,
// raw-byte HMAC verification, fixed autonomous identity, and durable delivery
// deduplication all cross HTTP and the subprocess boundary.
func (h *harness) testGitHubWebhookIngress(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	providerID := h.createWebhookProvider(t, ctx, fake.baseURL())
	agentID := h.createWebhookAgent(t, ctx, providerID+"/"+modelID)
	ownerID := h.registerWebhookOwner(t, ctx)
	h.assignWebhookAgent(t, ctx, agentID, ownerID)
	channelID := h.createGitHubWebhookChannel(t, ctx, agentID)
	endpoint := h.issueGitHubWebhookEndpoint(t, ctx, channelID, ownerID)

	body := []byte(`{"repository":{"full_name":"acme/system-test"},"ref":"refs/heads/main"}`)
	firstDeliveryID := "github-webhook-first-" + h.runID
	firstReply := "github webhook accepted " + h.runID
	fake.enqueueText(firstReply)

	if code := h.postGitHubDelivery(t, ctx, endpoint.URL, endpoint.Secret, firstDeliveryID, body, true); code != http.StatusAccepted {
		t.Fatalf("valid GitHub delivery = %d, want 202\n%s", code, h.proc.logTail(40))
	}

	sessionID := agent.BuildUserSessionKey(agentID, ownerID, "webhook:"+endpoint.ID)
	h.awaitWebhookRequests(t, fake, 1)
	if got := fake.requests()[0].Model; got != modelID {
		t.Fatalf("webhook model request = %q, want configured agent model %q", got, modelID)
	}
	h.assertWebhookSession(t, ctx, sessionID, ownerID, agentID, 1, firstReply)
	h.assertWebhookDeliveryCount(t, ctx, endpoint.ID, firstDeliveryID, 1)

	// A replay has the same signed raw bytes and delivery id. It is accepted so
	// GitHub stops retrying, but cannot admit another turn.
	if code := h.postGitHubDelivery(t, ctx, endpoint.URL, endpoint.Secret, firstDeliveryID, body, true); code != http.StatusAccepted {
		t.Fatalf("duplicate GitHub delivery = %d, want 202\n%s", code, h.proc.logTail(40))
	}
	h.assertWebhookRequestCount(t, fake, 1)
	h.assertWebhookSession(t, ctx, sessionID, ownerID, agentID, 1, firstReply)
	h.assertWebhookDeliveryCount(t, ctx, endpoint.ID, firstDeliveryID, 1)

	invalidDeliveryID := "github-webhook-invalid-" + h.runID
	if code := h.postGitHubDelivery(t, ctx, endpoint.URL, endpoint.Secret, invalidDeliveryID, body, false); code != http.StatusUnauthorized {
		t.Fatalf("invalid GitHub signature = %d, want 401\n%s", code, h.proc.logTail(40))
	}
	h.assertWebhookRequestCount(t, fake, 1)
	h.assertWebhookDeliveryCount(t, ctx, endpoint.ID, invalidDeliveryID, 0)

	// This busy proof is deterministic at subprocess level: the fake has already
	// received the first persistent-session model request and holds its stream
	// open, while Runtime.ChatAdmitted retains that session's synchronous guard.
	// A second delivery must get a retryable response, release its claim, then
	// admit exactly once when GitHub redelivers after the first turn completes.
	gatedDeliveryID := "github-webhook-gated-" + h.runID
	gatedFirst := "busy-first-" + h.runID
	gatedSecond := "busy-second-" + h.runID
	gate := fake.enqueueGatedText(gatedFirst, gatedSecond)
	if code := h.postGitHubDelivery(t, ctx, endpoint.URL, endpoint.Secret, gatedDeliveryID, body, true); code != http.StatusAccepted {
		t.Fatalf("gated GitHub delivery = %d, want 202\n%s", code, h.proc.logTail(40))
	}
	h.awaitWebhookRequests(t, fake, 2)

	busyDeliveryID := "github-webhook-busy-" + h.runID
	if code := h.postGitHubDelivery(t, ctx, endpoint.URL, endpoint.Secret, busyDeliveryID, body, true); code != http.StatusServiceUnavailable {
		t.Fatalf("busy GitHub delivery = %d, want 503\n%s", code, h.proc.logTail(40))
	}
	h.assertWebhookDeliveryCount(t, ctx, endpoint.ID, busyDeliveryID, 0)

	gate.Release()
	h.awaitWebhookAssistantContent(t, ctx, sessionID, 2, gatedFirst+gatedSecond)

	retryReply := "github webhook retry " + h.runID
	fake.enqueueText(retryReply)
	h.redeliverGitHubUntilAccepted(t, ctx, endpoint.URL, endpoint.Secret, busyDeliveryID, body)
	h.awaitWebhookRequests(t, fake, 3)
	h.awaitWebhookAssistantCount(t, ctx, sessionID, 3)
	h.assertWebhookDeliveryCount(t, ctx, endpoint.ID, busyDeliveryID, 1)
}

type issuedGitHubWebhookEndpoint struct {
	ID     string
	URL    string
	Secret string
}

func (h *harness) createWebhookProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	id := "anthropic-webhook-" + h.runID
	resp := h.postJSON(t, ctx, "/api/providers", map[string]any{
		"id": id, "type": "anthropic", "name": id, "enabled": true,
		"api_key": "system-test-not-a-secret", "base_url": baseURL,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST webhook provider = %d, want 201\n%s", resp.StatusCode, h.proc.logTail(40))
	}
	return id
}

func (h *harness) createWebhookAgent(t *testing.T, ctx context.Context, model string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents", map[string]any{
		"name": "sys-test-webhook-agent-" + h.runID, "model": model, "enabled": true,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST webhook agent = %d, want 201\n%s", resp.StatusCode, h.proc.logTail(40))
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

// registerWebhookOwner uses the real public registration flow with an isolated
// cookie jar. The harness enables local registration only inside its explicit
// subprocess environment so this fixture is a normal user, not the bootstrap
// admin reused for control-plane calls.
func (h *harness) registerWebhookOwner(t *testing.T, ctx context.Context) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create webhook-owner cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}
	payload, err := json.Marshal(map[string]string{
		"name":             "Webhook Owner " + h.runID,
		"email":            "webhook-owner-" + h.runID + "@system.test",
		"password":         "system-test-" + h.runID,
		"confirm_password": "system-test-" + h.runID,
	})
	if err != nil {
		t.Fatalf("marshal webhook owner: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/api/auth/local/register", bytes.NewReader(payload))
	if err != nil {
		t.Fatal("build webhook-owner registration request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("register webhook owner")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("POST webhook-owner registration = %d, want 200\n%s", resp.StatusCode, h.proc.logTail(40))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/auth/me", nil)
	if err != nil {
		t.Fatal("build webhook-owner identity request")
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal("get webhook-owner identity")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET webhook-owner identity = %d, want 200\n%s", resp.StatusCode, h.proc.logTail(40))
	}
	var owner struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&owner); err != nil {
		t.Fatalf("decode webhook-owner identity: %v", err)
	}
	if owner.ID == "" || owner.Role != "user" {
		t.Fatalf("webhook owner = id=%q role=%q, want normal active user", owner.ID, owner.Role)
	}
	return owner.ID
}

func (h *harness) assignWebhookAgent(t *testing.T, ctx context.Context, agentID, ownerID string) {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents/"+agentID+"/users", map[string]string{"user_id": ownerID})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST webhook agent assignment = %d, want 201\n%s", resp.StatusCode, h.proc.logTail(40))
	}
}

func (h *harness) createGitHubWebhookChannel(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	id := "github-webhook-" + h.runID
	config, err := json.Marshal(map[string]any{
		"provider": "github", "github_events": []string{"push"},
		"github_repositories": []string{"acme/system-test"}, "session_mode": "persistent",
	})
	if err != nil {
		t.Fatalf("marshal GitHub webhook config: %v", err)
	}
	resp := h.postJSON(t, ctx, "/api/channels", map[string]any{
		"id": id, "name": id, "type": "webhook", "agent_id": agentID, "config": string(config),
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST GitHub webhook channel = %d, want 201\n%s", resp.StatusCode, h.proc.logTail(40))
	}
	return id
}

func (h *harness) issueGitHubWebhookEndpoint(t *testing.T, ctx context.Context, channelID, ownerID string) issuedGitHubWebhookEndpoint {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/channels/"+channelID+"/webhook-endpoint", map[string]string{
		"owner_user_id": ownerID, "provider": "github",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST GitHub webhook endpoint = %d, want 201\n%s", resp.StatusCode, h.proc.logTail(40))
	}
	var issued struct {
		Endpoint struct {
			ID string `json:"id"`
		} `json:"endpoint"`
		URL    string `json:"url"`
		Secret string `json:"github_webhook_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		t.Fatalf("decode GitHub webhook endpoint: %v", err)
	}
	if issued.Endpoint.ID == "" || issued.URL == "" || issued.Secret == "" {
		t.Fatal("issued GitHub webhook endpoint is missing a one-time credential field")
	}
	return issuedGitHubWebhookEndpoint{ID: issued.Endpoint.ID, URL: issued.URL, Secret: issued.Secret}
}

// postGitHubDelivery intentionally uses a jar-less client and never reports the
// capability URL on a transport failure, keeping one-time credentials out of
// test diagnostics as well as production logs.
func (h *harness) postGitHubDelivery(t *testing.T, ctx context.Context, endpointURL, secret, deliveryID string, body []byte, valid bool) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal("build GitHub delivery request")
	}
	signature := "sha256=" + hex.EncodeToString(make([]byte, sha256.Size))
	if valid {
		signature = githubSignature(secret, body)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", signature)
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal("POST GitHub delivery")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func githubSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (h *harness) awaitWebhookRequests(t *testing.T, fake *fakeAnthropic, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if len(fake.requests()) >= want {
			h.assertWebhookRequestCount(t, fake, want)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake received %d model requests, want %d within 20s\n%s", len(fake.requests()), want, h.proc.logTail(40))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) assertWebhookRequestCount(t *testing.T, fake *fakeAnthropic, want int) {
	t.Helper()
	if got := len(fake.requests()); got != want {
		t.Fatalf("fake received %d model requests, want exactly %d\n%s", got, want, h.proc.logTail(40))
	}
}

func (h *harness) assertWebhookSession(t *testing.T, ctx context.Context, sessionID, ownerID, agentID string, assistantCount int, firstReply string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var (
			foundID, channel, kind, foundOwner, foundAgent string
			archived                                       bool
		)
		err := h.db.QueryRow(ctx,
			`SELECT session_id, channel, kind, coalesce(user_id, ''), coalesce(agent_id, ''), archived
			   FROM ctx_conversation
			  WHERE session_id = $1`, sessionID,
		).Scan(&foundID, &channel, &kind, &foundOwner, &foundAgent, &archived)
		if err == nil {
			if foundID != sessionID || channel != "webhook" || kind != "chat" || foundOwner != ownerID || foundAgent != agentID || archived {
				t.Fatalf("webhook session = id=%q channel=%q kind=%q owner=%q agent=%q archived=%t, want fixed live webhook chat session", foundID, channel, kind, foundOwner, foundAgent, archived)
			}
			var sessions int
			if err := h.db.QueryRow(ctx,
				`SELECT count(*) FROM ctx_conversation WHERE channel = 'webhook' AND user_id = $1 AND agent_id = $2`, ownerID, agentID,
			).Scan(&sessions); err != nil {
				t.Fatalf("count webhook sessions: %v", err)
			}
			if sessions != 1 {
				t.Fatalf("webhook sessions for fixed owner/agent = %d, want 1", sessions)
			}
			h.awaitWebhookAssistantCount(t, ctx, sessionID, assistantCount)
			if got := h.messageContent(t, ctx, sessionID, "assistant"); got != firstReply {
				t.Fatalf("first webhook assistant message = %q, want %q", got, firstReply)
			}
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("query webhook session %q: %v", sessionID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook session %q was not persisted within 20s: %v\n%s", sessionID, err, h.proc.logTail(40))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) awaitWebhookAssistantCount(t *testing.T, ctx context.Context, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var got int
		err := h.db.QueryRow(ctx,
			`SELECT count(*) FROM ctx_message m JOIN ctx_conversation c ON c.id = m.conversation_id WHERE c.session_id = $1 AND m.role = 'assistant'`, sessionID,
		).Scan(&got)
		if err != nil {
			t.Fatalf("count webhook assistant messages: %v", err)
		}
		if got == want {
			return
		}
		if got > want || time.Now().After(deadline) {
			t.Fatalf("webhook assistant messages = %d, want %d\n%s", got, want, h.proc.logTail(40))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitWebhookAssistantContent waits for the gated stream's terminal text, not
// merely its first persisted delta. A stream can persist that first delta before
// Runtime releases its session guard, so this is the completion precondition for
// the busy-delivery redelivery loop below.
func (h *harness) awaitWebhookAssistantContent(t *testing.T, ctx context.Context, sessionID string, wantCount int, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		h.awaitWebhookAssistantCount(t, ctx, sessionID, wantCount)
		var got string
		err := h.db.QueryRow(ctx,
			`SELECT m.content FROM ctx_message m JOIN ctx_conversation c ON c.id = m.conversation_id WHERE c.session_id = $1 AND m.role = 'assistant' ORDER BY m.seq DESC LIMIT 1`, sessionID,
		).Scan(&got)
		if err != nil {
			t.Fatalf("read final webhook assistant message: %v", err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("final webhook assistant message = %q, want %q\n%s", got, want, h.proc.logTail(40))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// redeliverGitHubUntilAccepted models GitHub's retry contract after a busy
// pre-admission rejection. A 503 proves the preceding claim was released; only
// the eventual 202 may consume the one queued fake response and retain a claim.
func (h *harness) redeliverGitHubUntilAccepted(t *testing.T, ctx context.Context, endpointURL, secret, deliveryID string, body []byte) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		switch code := h.postGitHubDelivery(t, ctx, endpointURL, secret, deliveryID, body, true); code {
		case http.StatusAccepted:
			return
		case http.StatusServiceUnavailable:
			if time.Now().After(deadline) {
				t.Fatalf("GitHub redelivery stayed busy for 20s\n%s", h.proc.logTail(40))
			}
			time.Sleep(50 * time.Millisecond)
		default:
			t.Fatalf("GitHub redelivery = %d, want 202 or retryable 503\n%s", code, h.proc.logTail(40))
		}
	}
}

func (h *harness) assertWebhookDeliveryCount(t *testing.T, ctx context.Context, endpointID, deliveryID string, want int) {
	t.Helper()
	var got int
	if err := h.db.QueryRow(ctx,
		`SELECT count(*) FROM channel_webhook_delivery WHERE endpoint_id = $1 AND provider = 'github' AND delivery_id = $2`, endpointID, deliveryID,
	).Scan(&got); err != nil {
		t.Fatalf("count GitHub delivery claim: %v", err)
	}
	if got != want {
		t.Fatalf("GitHub delivery claims = %d, want %d", got, want)
	}
}
