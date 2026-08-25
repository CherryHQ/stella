//go:build system

package system

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const distributedRunBudget = 75 * time.Second

// testDistributedRunRecoveryAndRemoteSSE exercises the process seams that an
// in-process lease test cannot prove. Two independent stellad processes share
// PostgreSQL while A owns each turn: A streams locally, B reports the durable
// remote owner and polls to the same transcript, and an abort sent through B is
// observed without any process-local notification. Finally A is killed in the
// boundary after the transcript commits but before AgentRun completion; restart
// terminalizes the old Run without replaying the model or source effects.
func (h *harness) testDistributedRunRecoveryAndRemoteSSE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*distributedRunBudget)
	defer cancel()

	fake := newFakeAnthropic(t)
	first := "remote-primary-one-" + h.runID + " "
	second := "remote-primary-two-" + h.runID
	completionGate := fake.enqueueGatedText(first, second)
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-distributed-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/claude-sonnet-4-6", "sys-test-distributed-agent-"+h.runID)
	sessionID := h.createSession(t, ctx, agentID)

	// B starts from the same durable deployment state but owns no listener-local
	// publisher, active turn, SSE hub, or cancellation function from A.
	replicaB, baseURLB := startServer(t, t, h.runID, h.generation+1000, h.home, h.dsn, h.vaultKey)
	eventsPath := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, sessionID)

	primary := h.openChatStream(t, ctx, agentID, sessionID, "prove remote polling "+h.runID)
	primaryScanner := newSSEScanner(primary)
	primaryText := readUntilTextDelta(t, primaryScanner)
	if primaryText != first {
		t.Fatalf("primary first delta = %q, want %q", primaryText, first)
	}

	local := h.openEventsStream(t, ctx, h.baseURL, eventsPath, http.StatusOK)
	localScanner := newSSEScanner(local)

	remoteRunID := h.assertRemoteEvents(t, ctx, baseURLB, eventsPath, "")
	var durableOwner string
	if err := h.db.QueryRow(ctx, `SELECT id FROM agent_run WHERE session_id = $1 AND status = 'running'`, sessionID).Scan(&durableOwner); err != nil {
		t.Fatalf("query remote AgentRun owner: %v", err)
	}
	if remoteRunID != durableOwner {
		t.Fatalf("remote 503 run_id = %q, durable owner = %q", remoteRunID, durableOwner)
	}
	// Poll B again while A is still gated. This proves the documented remote
	// client loop observes a stable durable owner, rather than merely checking
	// one synthetic 503 before jumping directly to terminal state.
	h.assertRemoteEvents(t, ctx, baseURLB, eventsPath, remoteRunID)

	completionGate.Release()
	primaryText += collectSSEText(t, primaryScanner)
	localText := collectSSEText(t, localScanner)
	_ = primary.Body.Close()
	_ = local.Body.Close()
	if want := first + second; primaryText != want || localText != want {
		t.Fatalf("local primary/attach text = %q/%q, want %q", primaryText, localText, want)
	}

	h.pollRemoteEventsToNoContent(t, ctx, baseURLB, eventsPath, remoteRunID)
	h.assertHistoryTextFromReplica(t, ctx, baseURLB, agentID, sessionID, first+second)
	status, _ := h.awaitAgentRunTerminal(t, ctx, remoteRunID)
	if status != "completed" {
		t.Fatalf("remote-polled AgentRun status = %q, want completed", status)
	}

	// Abort through B. B has no local cancel function for A's turn, so this
	// proves the durable abort/heartbeat/reaper path rather than same-process
	// notification. Committing abort before opening the provider gate also
	// linearizes completion against it deterministically.
	abortGate := fake.enqueueGatedText("remote-abort-first-"+h.runID, "remote-abort-second-"+h.runID)
	abortSessionID := h.createSession(t, ctx, agentID)
	abortStream := h.openChatStream(t, ctx, agentID, abortSessionID, "abort remotely "+h.runID)
	abortScanner := newSSEScanner(abortStream)
	_ = readUntilTextDelta(t, abortScanner)
	abortEventsPath := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, abortSessionID)
	abortRunID := h.assertRemoteEvents(t, ctx, baseURLB, abortEventsPath, "")
	stopReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURLB+fmt.Sprintf("/api/agents/%s/sessions/%s/stop", agentID, abortSessionID), nil)
	if err != nil {
		t.Fatalf("build remote stop: %v", err)
	}
	stopResp, err := h.client.Do(stopReq)
	if err != nil {
		t.Fatalf("POST remote stop: %v\n%s", err, replicaB.logTail(40))
	}
	_ = stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST remote stop = %d, want 204\n%s", stopResp.StatusCode, replicaB.logTail(40))
	}
	abortGate.Release()
	status, _ = h.awaitAgentRunTerminal(t, ctx, abortRunID)
	if status != "aborted" {
		t.Fatalf("remote-aborted AgentRun status = %q, want aborted", status)
	}
	_ = abortStream.Body.Close()
	h.pollRemoteEventsToNoContent(t, ctx, baseURLB, abortEventsPath, abortRunID)

	// B is no longer needed. Stopping it before the crash proof leaves the
	// replacement process as the sole reaper and also exercises ordered control
	// leadership handoff while A remains ready.
	replicaB.stop(t)

	crashFirst := "crash-before-terminal-one-" + h.runID + " "
	crashSecond := "crash-before-terminal-two-" + h.runID
	crashGate := fake.enqueueGatedText(crashFirst, crashSecond)
	crashSessionID := h.createSession(t, ctx, agentID)
	crashStream := h.openChatStream(t, ctx, agentID, crashSessionID, "crash boundary "+h.runID)
	crashScanner := newSSEScanner(crashStream)
	crashText := readUntilTextDelta(t, crashScanner)

	// A test-owned trigger gates only the terminal AgentRun UPDATE on a PostgreSQL
	// advisory lock. Transcript and session-completion writes retain their normal
	// transaction-coupled ownership fence and remain free to commit. This creates
	// the exact source/session-complete / Run-nonterminal kill window in the real
	// binary without a production failpoint.
	const finishLockKey int64 = 1035
	lockConn, err := h.db.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire AgentRun finish gate connection: %v", err)
	}
	defer lockConn.Release()
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, finishLockKey); err != nil {
		t.Fatalf("lock AgentRun finish gate: %v", err)
	}
	var crashRunID string
	if err := h.db.QueryRow(ctx, `
		SELECT id FROM agent_run
		WHERE session_id = $1 AND status = 'running'
	`, crashSessionID).Scan(&crashRunID); err != nil {
		t.Fatalf("query running AgentRun: %v", err)
	}
	if _, err := h.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS system_test_agent_run_finish_gate (
		run_id UUID PRIMARY KEY,
		lock_key BIGINT NOT NULL
	)`); err != nil {
		t.Fatalf("create AgentRun finish gate table: %v", err)
	}
	if _, err := h.db.Exec(ctx, `
			CREATE OR REPLACE FUNCTION system_test_gate_agent_run_finish()
		RETURNS trigger LANGUAGE plpgsql AS $body$
		DECLARE gate_key BIGINT;
		BEGIN
			SELECT lock_key INTO gate_key
			FROM system_test_agent_run_finish_gate
			WHERE run_id = NEW.id;
			IF FOUND THEN
				PERFORM pg_advisory_xact_lock(gate_key);
			END IF;
			RETURN NEW;
		END
		$body$;
	`); err != nil {
		t.Fatalf("create AgentRun finish gate function: %v", err)
	}
	if _, err := h.db.Exec(ctx, `DROP TRIGGER IF EXISTS system_test_gate_agent_run_finish ON agent_run`); err != nil {
		t.Fatalf("drop stale AgentRun finish gate trigger: %v", err)
	}
	if _, err := h.db.Exec(ctx, `
		CREATE TRIGGER system_test_gate_agent_run_finish
			BEFORE UPDATE OF status ON agent_run
			FOR EACH ROW
			WHEN (OLD.status = 'running' AND NEW.status <> 'running')
			EXECUTE FUNCTION system_test_gate_agent_run_finish()
	`); err != nil {
		t.Fatalf("create AgentRun finish gate trigger: %v", err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO system_test_agent_run_finish_gate (run_id, lock_key)
		VALUES ($1, $2)
		ON CONFLICT (run_id) DO UPDATE SET lock_key = EXCLUDED.lock_key
	`, crashRunID, finishLockKey); err != nil {
		t.Fatalf("install AgentRun finish gate: %v", err)
	}
	crashGate.Release()
	crashText = readUntilText(t, crashScanner, crashText, crashFirst+crashSecond)
	if want := crashFirst + crashSecond; crashText != want {
		t.Fatalf("crash-boundary primary text = %q, want %q", crashText, want)
	}
	h.awaitCompletedTurnEffects(t, ctx, crashSessionID, agentID, "crash boundary "+h.runID, crashFirst+crashSecond)
	var preCrashStatus string
	if err := h.db.QueryRow(ctx, `SELECT status FROM agent_run WHERE id = $1`, crashRunID).Scan(&preCrashStatus); err != nil || preCrashStatus != "running" {
		t.Fatalf("pre-crash AgentRun status = %q err=%v, want running", preCrashStatus, err)
	}

	old := h.proc
	old.forceCrash(t)
	_ = crashStream.Body.Close()
	// A PostgreSQL backend blocked inside an autocommit statement can outlive a
	// suddenly closed client socket until the lock wakes it; if we unlocked first,
	// that orphaned backend could commit the terminal UPDATE after stellad died.
	// Terminate only the test-gated CompleteAgentRun backend while the lock is
	// still held, preserving the intended pre-terminal crash boundary.
	if _, err := h.db.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		  AND datname = current_database()
		  AND state = 'active'
		  AND wait_event_type = 'Lock'
		  AND query LIKE '%-- name: CompleteAgentRun :execrows%'
	`); err != nil {
		t.Fatalf("terminate orphaned AgentRun completion backend: %v", err)
	}
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, finishLockKey); err != nil {
		t.Fatalf("unlock AgentRun finish gate: %v", err)
	}
	if err := h.db.QueryRow(ctx, `SELECT status FROM agent_run WHERE id = $1`, crashRunID).Scan(&preCrashStatus); err != nil || preCrashStatus != "running" {
		t.Fatalf("post-crash AgentRun status = %q err=%v, want running before recovery", preCrashStatus, err)
	}
	if _, err := h.db.Exec(ctx, `
		DROP TRIGGER IF EXISTS system_test_gate_agent_run_finish ON agent_run;
		DROP FUNCTION IF EXISTS system_test_gate_agent_run_finish();
		DROP TABLE IF EXISTS system_test_agent_run_finish_gate;
	`); err != nil {
		t.Fatalf("remove AgentRun finish gate: %v", err)
	}
	h.generation++
	proc, baseURL := startServer(t, h.owner, h.runID, h.generation, h.home, h.dsn, h.vaultKey)
	h.proc, h.baseURL = proc, baseURL

	status, reason := h.awaitAgentRunTerminal(t, ctx, crashRunID)
	if status != "interrupted" || reason != "lease_expired" {
		t.Fatalf("recovered crash AgentRun = %q/%q, want interrupted/lease_expired", status, reason)
	}
	h.assertExactTurnEffects(t, ctx, crashSessionID)
	if got := len(fake.requests()); got != 3 {
		t.Fatalf("model requests after abort/crash recovery = %d, want exactly 3 (no replay)", got)
	}
	crashEventsPath := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, crashSessionID)
	h.pollRemoteEventsToNoContent(t, ctx, h.baseURL, crashEventsPath, crashRunID)

	// A second forced process death crosses the real sandbox seam: the model
	// starts a long-lived bash child, stellad is SIGKILLed while the tool is
	// blocked, startup recovery reconstructs provider cleanup from the durable
	// resource ID, and only then may the same Session allocate generation N+1.
	h.proveSandboxChildCrashRecovery(t, ctx, fake, agentID)
}

func (h *harness) proveSandboxChildCrashRecovery(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("durable host-process marker proof requires Linux /proc")
	}
	sessionID := h.createSession(t, ctx, agentID)
	childToken := "stella-1035-sandbox-child-" + h.runID
	// The child deliberately drops the durable environment marker and creates a
	// new process group. Local bwrap's PID namespace must still make the child a
	// member of the generation and destroy it when its namespace init dies.
	command := fmt.Sprintf("env -u STELLA_SANDBOX_RESOURCE_ID setsid bash -c 'exec -a %s sleep 300' & wait", childToken)
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	fake.enqueueTool("toolu_sandbox_crash", "bash", string(args))
	stream := h.openChatStream(t, ctx, agentID, sessionID, "start sandbox child "+h.runID)
	_ = fake.waitForRequests(ctx, 4)

	var runID, resourceID, state string
	var generation int64
	deadline := time.Now().Add(distributedRunBudget)
	for {
		err = h.db.QueryRow(ctx, `
			SELECT r.id, s.generation, s.resource_id, s.state
			FROM agent_run r
			JOIN agent_session_sandbox s ON s.session_id = r.session_id AND s.run_id = r.id
			WHERE r.session_id = $1 AND r.status = 'running'
		`, sessionID).Scan(&runID, &generation, &resourceID, &state)
		if err == nil && (state == "creating" || state == "active") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandbox child did not become durable: state=%q err=%v", state, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The durable row is claimed before provider Create returns, so state
	// "creating" can legitimately become visible before the host process has
	// exec'd with its resource marker. Wait for both sides of that boundary
	// rather than making scheduler timing part of the crash proof.
	for len(markedSandboxProcessIDs(resourceID)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no live process carries durable sandbox marker %s", resourceID)
		}
		time.Sleep(25 * time.Millisecond)
	}
	var oldChildPIDs []int
	for {
		oldChildPIDs = processIDsWithCommandToken(childToken)
		if len(oldChildPIDs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached markerless sandbox child %q did not start", childToken)
		}
		time.Sleep(25 * time.Millisecond)
	}

	old := h.proc
	old.forceCrash(t)
	_ = stream.Body.Close()
	h.generation++
	proc, baseURL := startServer(t, h.owner, h.runID, h.generation, h.home, h.dsn, h.vaultKey)
	h.proc, h.baseURL = proc, baseURL

	status, reason := h.awaitAgentRunTerminal(t, ctx, runID)
	if status != "interrupted" || reason != "lease_expired" {
		t.Fatalf("sandbox crash AgentRun = %q/%q, want interrupted/lease_expired", status, reason)
	}
	for {
		err = h.db.QueryRow(ctx, `SELECT state FROM agent_session_sandbox WHERE session_id = $1`, sessionID).Scan(&state)
		if err == nil && state == "destroyed" && len(markedSandboxProcessIDs(resourceID)) == 0 && len(processIDsWithCommandToken(childToken)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old sandbox resource was not proven absent: state=%q marked=%v child=%v original_child=%v err=%v", state, markedSandboxProcessIDs(resourceID), processIDsWithCommandToken(childToken), oldChildPIDs, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	replacementText := "sandbox replacement complete " + h.runID
	fake.enqueueTool("toolu_sandbox_replacement", "bash", `{"command":"printf replacement"}`)
	fake.enqueueText(replacementText)
	replacement := h.openChatStream(t, ctx, agentID, sessionID, "replace sandbox after crash "+h.runID)
	got := collectSSEText(t, newSSEScanner(replacement))
	_ = replacement.Body.Close()
	if got != replacementText {
		t.Fatalf("replacement sandbox reply = %q, want %q", got, replacementText)
	}
	var replacementGeneration int64
	var replacementResource string
	if err := h.db.QueryRow(ctx, `
		SELECT generation, resource_id FROM agent_session_sandbox WHERE session_id = $1
	`, sessionID).Scan(&replacementGeneration, &replacementResource); err != nil {
		t.Fatal(err)
	}
	if replacementGeneration != generation+1 || replacementResource == resourceID {
		t.Fatalf("replacement sandbox = generation %d resource %q, want %d and distinct from %q", replacementGeneration, replacementResource, generation+1, resourceID)
	}
	if got := len(fake.requests()); got != 6 {
		t.Fatalf("model requests after sandbox crash/replacement = %d, want 6 (no old tool replay)", got)
	}
}

func markedSandboxProcessIDs(resourceID string) []int {
	marker := []byte("STELLA_SANDBOX_RESOURCE_ID=" + resourceID)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		for value := range bytes.SplitSeq(environ, []byte{0}) {
			if bytes.Equal(value, marker) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

func processIDsWithCommandToken(token string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && bytes.Contains(cmdline, []byte(token)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func readUntilTextDelta(t *testing.T, scanner *bufio.Scanner) string {
	t.Helper()
	for {
		event, done := scanTurnEvent(t, scanner)
		if done {
			t.Fatal("SSE completed before a text delta")
		}
		if event.Type == "error" {
			t.Fatalf("SSE error before text: %s", event.ErrorText)
		}
		if event.Type == "text-delta" && event.Delta != "" {
			return event.Delta
		}
	}
}

func collectSSEText(t *testing.T, scanner *bufio.Scanner) string {
	t.Helper()
	var text strings.Builder
	for {
		event, done := scanTurnEvent(t, scanner)
		if event.Type == "error" {
			t.Fatalf("SSE emitted error: %s", event.ErrorText)
		}
		if event.Type == "text-delta" {
			text.WriteString(event.Delta)
		}
		if done {
			return text.String()
		}
	}
}

func readUntilText(t *testing.T, scanner *bufio.Scanner, initial, want string) string {
	t.Helper()
	text := initial
	for text != want {
		event, done := scanTurnEvent(t, scanner)
		if event.Type == "error" {
			t.Fatalf("SSE emitted error: %s", event.ErrorText)
		}
		if event.Type == "text-delta" {
			text += event.Delta
			if !strings.HasPrefix(want, text) {
				t.Fatalf("SSE text %q is not prefix of %q", text, want)
			}
		}
		if done && text != want {
			t.Fatalf("SSE completed with text %q, want %q", text, want)
		}
	}
	return text
}

func (h *harness) openEventsStream(t *testing.T, ctx context.Context, baseURL, path string, wantStatus int) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatalf("build events request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("GET events = %d, want %d (%s)", resp.StatusCode, wantStatus, body)
	}
	return resp
}

func (h *harness) assertRemoteEvents(t *testing.T, ctx context.Context, baseURL, path, wantRunID string) string {
	t.Helper()
	resp := h.openEventsStream(t, ctx, baseURL, path, http.StatusServiceUnavailable)
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("remote Retry-After = %q, want 1", got)
	}
	var body struct {
		Error struct {
			Status  string `json:"status"`
			Details struct {
				RunID string `json:"run_id"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode remote SSE error: %v", err)
	}
	if body.Error.Status != "UNAVAILABLE" || body.Error.Details.RunID == "" {
		t.Fatalf("remote SSE error = %#v, want UNAVAILABLE with run_id", body.Error)
	}
	if wantRunID != "" && body.Error.Details.RunID != wantRunID {
		t.Fatalf("remote run_id = %q, want stable %q", body.Error.Details.RunID, wantRunID)
	}
	return body.Error.Details.RunID
}

func (h *harness) pollRemoteEventsToNoContent(t *testing.T, ctx context.Context, baseURL, path, runID string) {
	t.Helper()
	deadline := time.Now().Add(distributedRunBudget)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatalf("poll remote events: %v", err)
		}
		switch resp.StatusCode {
		case http.StatusNoContent:
			_ = resp.Body.Close()
			return
		case http.StatusServiceUnavailable:
			if got := resp.Header.Get("Retry-After"); got != "1" {
				_ = resp.Body.Close()
				t.Fatalf("remote poll Retry-After = %q, want 1", got)
			}
			var body struct {
				Error struct {
					Details struct {
						RunID string `json:"run_id"`
					} `json:"details"`
				} `json:"error"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if decodeErr != nil || body.Error.Details.RunID != runID {
				t.Fatalf("remote poll run_id = %q decode=%v, want stable %q", body.Error.Details.RunID, decodeErr, runID)
			}
			if time.Now().After(deadline) {
				t.Fatalf("remote events remained 503 for Run %s", runID)
			}
			time.Sleep(100 * time.Millisecond)
		default:
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("remote events poll = %d (%s), want 503 then 204", resp.StatusCode, body)
		}
	}
}

func (h *harness) assertHistoryTextFromReplica(t *testing.T, ctx context.Context, baseURL, agentID, sessionID, want string) {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET durable transcript from replica: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET durable transcript = %d (%s)", resp.StatusCode, body)
	}
	var history struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Blocks  []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("decode durable transcript: %v", err)
	}
	for _, message := range history.Messages {
		if message.Role != "assistant" {
			continue
		}
		got := message.Content
		if got == "" {
			for _, block := range message.Blocks {
				got += block.Text
			}
		}
		if got == want {
			return
		}
	}
	t.Fatalf("replica transcript has no assistant text %q: %#v", want, history.Messages)
}

func (h *harness) awaitAgentRunTerminal(t *testing.T, ctx context.Context, runID string) (status, reason string) {
	t.Helper()
	deadline := time.Now().Add(distributedRunBudget)
	for {
		var completed bool
		err := h.db.QueryRow(ctx, `
			SELECT status, terminal_reason, completed_at IS NOT NULL
			FROM agent_run WHERE id = $1
		`, runID).Scan(&status, &reason, &completed)
		if err != nil {
			t.Fatalf("query AgentRun %s: %v", runID, err)
		}
		if status != "running" {
			if !completed {
				t.Fatalf("terminal AgentRun %s has no completed_at", runID)
			}
			return status, reason
		}
		if time.Now().After(deadline) {
			t.Fatalf("AgentRun %s remained running beyond %s", runID, distributedRunBudget)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (h *harness) assertExactTurnEffects(t *testing.T, ctx context.Context, sessionID string) {
	t.Helper()
	var users, assistants int
	if err := h.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE m.role = 'user'),
		       count(*) FILTER (WHERE m.role = 'assistant')
		FROM ctx_message m
		JOIN ctx_conversation c ON c.id = m.conversation_id
		WHERE c.session_id = $1
	`, sessionID).Scan(&users, &assistants); err != nil {
		t.Fatalf("count crash turn effects: %v", err)
	}
	if users != 1 || assistants != 1 {
		t.Fatalf("crash turn user/assistant effects = %d/%d, want exactly 1/1", users, assistants)
	}
}

func (h *harness) awaitCompletedTurnEffects(t *testing.T, ctx context.Context, sessionID, agentID, userText, assistantText string) {
	t.Helper()
	deadline := time.Now().Add(distributedRunBudget)
	for {
		var result string
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(last_turn_result, '')
			FROM ctx_conversation
			WHERE session_id = $1 AND agent_id = $2
		`, sessionID, agentID).Scan(&result)
		if err == nil && result == "success" {
			h.assertChatRowsPersisted(t, ctx, sessionID, agentID, userText, assistantText)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s did not commit successful completion before crash: result=%q err=%v", sessionID, result, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
