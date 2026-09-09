//go:build system

package system

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// This crosses the HTTP cancellation and process-restart seams. A local cancel
// function alone cannot preserve the run's terminal result after restart.
func (h *harness) testChatAbortPersistsAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	fake := newFakeAnthropic(t)
	gate := fake.enqueueGatedText("started "+h.runID, "must not complete")
	release := sync.OnceFunc(func() { close(gate.release) })
	defer release()
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-abort-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "sys-abort-"+h.runID)
	sessionID := h.createSession(t, ctx, agentID)
	stream := h.openChatStream(t, ctx, agentID, sessionID, "stop this turn")
	defer func() { _ = stream.Body.Close() }()
	scanner := newSSEScanner(stream)
	for {
		event, done := scanTurnEvent(t, scanner)
		if done {
			t.Fatal("turn completed before the model gate")
		}
		if event.Type == "text-delta" && event.Delta != "" {
			break
		}
	}
	var runID string
	if err := h.db.QueryRow(ctx, "SELECT id FROM agent_run WHERE session_id = $1 AND status = 'running'", sessionID).Scan(&runID); err != nil {
		t.Fatalf("read admitted run: %v", err)
	}
	stop := h.postJSON(t, ctx, fmt.Sprintf("/api/agents/%s/sessions/%s/stop", agentID, sessionID), nil)
	_ = stop.Body.Close()
	if stop.StatusCode != http.StatusNoContent {
		t.Fatalf("stop returned %d, want 204", stop.StatusCode)
	}
	for {
		_, done := scanTurnEvent(t, scanner)
		if done {
			break
		}
	}
	release()
	h.awaitAbortedRun(t, ctx, sessionID, runID)
	requests := fake.requestCount()
	h.restartAfterForcedCrash(t)
	h.awaitAbortedRun(t, ctx, sessionID, runID)
	if got := fake.requestCount(); got != requests {
		t.Fatalf("restart replayed model work: requests = %d, before restart %d", got, requests)
	}

	// A forced crash has no absence proof for the retained native sandbox.
	// Reject this Session without touching the model until compute recovery has
	// completed; an aborted Run alone cannot authorize a replacement sandbox.
	blocked := h.openChatStream(t, ctx, agentID, sessionID, "must remain fenced")
	body, err := io.ReadAll(blocked.Body)
	_ = blocked.Body.Close()
	if err != nil || !strings.Contains(string(body), "sandbox generation") {
		t.Fatalf("crashed compute was not fenced: body=%s err=%v", body, err)
	}
	if got := fake.requestCount(); got != requests {
		t.Fatalf("fenced compute reached the model: requests=%d, before=%d", got, requests)
	}

	// Compute recovery is scoped to the affected Session. A separate Session
	// can acquire a fresh Run without reviving or rewriting the canceled owner.
	successorSessionID := h.createSession(t, ctx, agentID)
	fake.enqueueText("successor " + h.runID)
	_, reply := h.streamChatTurn(t, ctx, agentID, successorSessionID, "start a fresh turn")
	if reply != "successor "+h.runID {
		t.Fatalf("successor reply = %q", reply)
	}
	var count int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM agent_run WHERE session_id = $1 AND id <> $2 AND status = 'completed'", successorSessionID, runID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("completed successor count = %d, err = %v", count, err)
	}
	var originalStatus string
	if err := h.db.QueryRow(ctx, "SELECT status FROM agent_run WHERE id = $1", runID).Scan(&originalStatus); err != nil || originalStatus != "aborted" {
		t.Fatalf("original Run changed after restart: status=%s err=%v", originalStatus, err)
	}
}

func (h *harness) awaitAbortedRun(t *testing.T, ctx context.Context, sessionID, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status, result string
		var terminalActivity bool
		err := h.db.QueryRow(ctx, `SELECT r.status, coalesce(c.last_turn_result, ''),
			c.last_turn_completed_at IS NOT NULL AND c.last_turn_completed_at >= c.last_turn_started_at
			FROM agent_run r JOIN ctx_conversation c ON c.session_id = r.session_id
			WHERE r.id = $1 AND r.session_id = $2`, runID, sessionID).Scan(&status, &result, &terminalActivity)
		if err != nil {
			t.Fatalf("read durable abort: %v", err)
		}
		if status == "aborted" && result == "canceled" && terminalActivity {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("abort did not persist: run=%q activity=%q completed=%v: %v", status, result, terminalActivity, ctx.Err())
		case <-ticker.C:
		}
	}
}
