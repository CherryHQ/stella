//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/test/testbed"
)

// TestRemoteSessionEventsAcrossProcesses proves the ownership boundary over
// two real stellad processes sharing one PostgreSQL database. A's live SSE
// stream remains the sole owner; B must report the durable run as remote until
// A writes the terminal result, after which B's read-only attach becomes 204.
func TestRemoteSessionEventsAcrossProcesses(t *testing.T) {
	skipUnsupportedHost(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	a, err := testbed.Start(ctx, testbed.Options{RepoRoot: repoRoot(t), Port: 0, FakeModel: true, Bootstrap: false})
	if err != nil {
		t.Fatalf("start primary testbed: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}
	db, err := pgxpool.New(ctx, a.DatabaseURL())
	if err != nil {
		t.Fatalf("connect assertion pool: %v", err)
	}
	t.Cleanup(db.Close)

	aHarness := &harness{
		owner:   t,
		runID:   newRunID(t),
		baseURL: a.BaseURL(),
		client:  client,
		db:      db,
		proc:    a,
	}
	aHarness.registerBootstrapUser(t, ctx)
	fake := &fakeAnthropic{Fake: a.Fake()}
	fake.Reset()
	providerID := aHarness.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-remote-"+aHarness.runID)
	agentID := aHarness.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "sys-remote-"+aHarness.runID)
	sessionID := aHarness.createSession(t, ctx, agentID)

	b, err := testbed.Start(ctx, testbed.Options{
		RepoRoot:    repoRoot(t),
		Port:        0,
		DatabaseURL: a.DatabaseURL(),
		Bootstrap:   false,
		FakeModel:   false,
	})
	if err != nil {
		t.Fatalf("start secondary testbed on shared database: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop() })
	bHarness := &harness{
		owner:   t,
		runID:   aHarness.runID,
		baseURL: b.BaseURL(),
		client:  client,
		db:      db,
		proc:    b,
	}

	first := "owned by primary " + aHarness.runID
	second := " and completed on primary"
	gate := fake.enqueueGatedText(first, second)
	release := func() { close(gate.release) }
	defer func() {
		// A failed assertion must still unblock the model request before cleanup.
		select {
		case <-gate.release:
		default:
			release()
		}
	}()

	stream := aHarness.openChatStream(t, ctx, agentID, sessionID, "cross process "+aHarness.runID)
	defer func() { _ = stream.Body.Close() }()
	scanner := newSSEScanner(stream)
	for {
		event, done := scanTurnEvent(t, scanner)
		if done {
			t.Fatal("primary stream completed before the model gate")
		}
		if event.Type == "text-delta" && event.Delta != "" {
			if event.Delta != first {
				t.Fatalf("primary first delta = %q, want %q", event.Delta, first)
			}
			break
		}
	}

	runID := waitRunningRunID(t, ctx, db, sessionID)
	remote := getSessionEvents(t, ctx, bHarness, agentID, sessionID)
	defer func() { _ = remote.Body.Close() }()
	if remote.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("secondary attach = %d, want 503: %s", remote.StatusCode, readBody(t, remote))
	}
	if got := remote.Header.Get("Retry-After"); got != "3" {
		t.Fatalf("secondary Retry-After = %q, want 3", got)
	}
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(remote.Body).Decode(&body); err != nil {
		t.Fatalf("decode secondary remote error: %v", err)
	}
	if got, _ := body.Error.Details["run_id"].(string); got != runID {
		t.Fatalf("secondary run_id = %q, want primary run %q", got, runID)
	}

	release()
	for {
		_, done := scanTurnEvent(t, scanner)
		if done {
			break
		}
	}

	waitForSessionEvents204(t, ctx, bHarness, agentID, sessionID)
	messages := getSessionMessages(t, ctx, bHarness, agentID, sessionID)
	if !bytes.Contains(messages, []byte(second)) {
		t.Fatalf("secondary transcript does not contain terminal text %q: %s", second, messages)
	}
}

func waitRunningRunID(t *testing.T, ctx context.Context, db *pgxpool.Pool, sessionID string) string {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var runID string
		err := db.QueryRow(ctx, "SELECT id FROM agent_run WHERE session_id = $1 AND status = 'running' ORDER BY created_at DESC LIMIT 1", sessionID).Scan(&runID)
		if err == nil {
			return runID
		}
		select {
		case <-ctx.Done():
			t.Fatalf("find running run: %v", ctx.Err())
		case <-deadline.C:
			t.Fatalf("running run did not become visible for session %q: %v", sessionID, err)
		case <-ticker.C:
		}
	}
}

func getSessionEvents(t *testing.T, ctx context.Context, h *harness, agentID, sessionID string) *http.Response {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build secondary attach request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("secondary attach: %v\n%s", err, h.proc.LogTail(40))
	}
	return resp
}

func waitForSessionEvents204(t *testing.T, ctx context.Context, h *harness, agentID, sessionID string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		resp := getSessionEvents(t, ctx, h, agentID, sessionID)
		if resp.StatusCode == http.StatusNoContent {
			_ = resp.Body.Close()
			return
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			status := resp.StatusCode
			_ = resp.Body.Close()
			t.Fatalf("secondary attach after terminalization = %d, want 503 while settling or 204", status)
		}
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			t.Fatalf("secondary attach after terminalization: %v", ctx.Err())
		case <-deadline.C:
			t.Fatalf("secondary attach did not settle to 204; last status %d", resp.StatusCode)
		case <-ticker.C:
		}
	}
}

func getSessionMessages(t *testing.T, ctx context.Context, h *harness, agentID, sessionID string) []byte {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatalf("build transcript request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("secondary transcript: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("secondary transcript = %d, want 200: %s", resp.StatusCode, readBody(t, resp))
	}
	return readBody(t, resp)
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read HTTP body: %v", err)
	}
	return body.Bytes()
}
