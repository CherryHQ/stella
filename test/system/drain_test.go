//go:build system

package system

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// drainFlipBudget bounds how long after SIGTERM the readiness probe may take to
// stop reporting ready, and how long the drain-cancelled attach stream may take
// to end. Both are near-instant in practice; the budget is generous so a slow CI
// host does not flake, yet far below the harness graceful-shutdown budget so a
// genuine hang still fails here rather than at teardown.
const drainFlipBudget = 15 * time.Second

// testGracefulDrain proves the four coexisting shutdown behaviors that only a
// real process over TCP can show, and it must run LAST because it consumes the
// shared server. With one turn deliberately pinned in flight (a gated fake
// response) at the moment SIGTERM arrives:
//
//   - readiness flips: /readyz stops reporting ready promptly (draining is set
//     before the listener is touched, so a probe can never see 200 again);
//
//   - an attach subscription is drain-cancelled: its read-only event stream ends
//     promptly rather than blocking the shutdown budget;
//
//   - the initiating send observer is drain-cancelled promptly while its
//     server-owned turn remains accepted work;
//
//   - once the gate releases, the detached turn completes and persists its full
//     reply before the process exits 0.
//
// The gated turn both pins real work across SIGTERM and gives the attach
// subscription something live to attach to (204 otherwise).
func (h *harness) testGracefulDrain(t *testing.T) {
	fake := newFakeAnthropic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// A two-part reply split by a gate: the fake flushes "part one", blocks, then
	// (once released) flushes "part two". The run-scoped text lets the completion
	// assertion below be exact.
	part1 := "drain-part-one-" + h.runID + " "
	part2 := "drain-part-two-" + h.runID
	gate := fake.enqueueGatedText(part1, part2)

	const modelID = "claude-sonnet-4-6"
	providerID := h.createDrainProvider(t, ctx, fake.baseURL())
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)
	sessionID := h.createSession(t, ctx, agentID)

	// 1. Start the send turn and read until its first text-delta arrives, proving
	//    the turn is genuinely streaming (and thus live for an attach subscriber)
	//    before it is pinned by the gate.
	sendResult := make(chan turnCompletion, 1)
	firstDelta := make(chan struct{}, 1)
	go h.runGatedSendTurn(t, ctx, agentID, sessionID, firstDelta, sendResult)
	select {
	case <-firstDelta:
	case <-time.After(30 * time.Second):
		t.Fatalf("send turn produced no text-delta within 30s; cannot pin an in-flight turn\n%s", h.proc.LogTail(40))
	}

	// 2. Attach to the same session's in-flight turn. The turn is live (mid-stream
	//    above), so the server must stream (200), not answer 204.
	attachEnded := make(chan time.Time, 1)
	go h.runAttachStream(t, ctx, agentID, sessionID, attachEnded)

	// 3. Begin readiness sampling on a hot keep-alive connection, then send
	//    SIGTERM. Sampling starts first so a request is in flight across the drain
	//    flip and can observe the 503 before the listener closes.
	if code := h.readyzStatus(t, ctx); code != http.StatusOK {
		t.Fatalf("/readyz = %d before drain, want 200 (server must be ready before SIGTERM)\n%s", code, h.proc.LogTail(40))
	}
	notReady := make(chan readyzFlip, 1)
	stopSampling := make(chan struct{})
	go sampleReadyzUntilNotReady(h.baseURL, stopSampling, notReady)

	sigAt := time.Now()
	if err := h.proc.Terminate(); err != nil {
		close(stopSampling)
		t.Fatalf("send SIGTERM to server: %v", err)
	}

	// 4. Readiness must flip away from ready promptly. Draining is set before the
	//    listener is touched, so a probe can never see 200 again — but this build
	//    closes the listener immediately (no in-process LB-propagation delay), so a
	//    black-box probe usually observes a closed listener rather than a 503 body.
	//    Both are the same not-ready fact; which was seen is logged for the record.
	var flip readyzFlip
	select {
	case flip = <-notReady:
	case <-time.After(drainFlipBudget):
		close(stopSampling)
		t.Fatalf("/readyz still ready %s after SIGTERM; drain did not flip readiness\n%s", drainFlipBudget, h.proc.LogTail(40))
	}
	close(stopSampling)
	if flip.status == http.StatusServiceUnavailable {
		t.Logf("graceful_drain: /readyz -> 503 %s after SIGTERM", flip.at.Sub(sigAt))
	} else {
		t.Logf("graceful_drain: /readyz became unreachable (listener closed) %s after SIGTERM: %v", flip.at.Sub(sigAt), flip.err)
	}

	// 5. The attach subscription is drain-cancelled: its stream must end promptly.
	var attachAt time.Time
	select {
	case attachAt = <-attachEnded:
	case <-time.After(drainFlipBudget):
		t.Fatalf("attach stream did not end within %s of SIGTERM; drain did not cancel it\n%s", drainFlipBudget, h.proc.LogTail(40))
	}
	t.Logf("graceful_drain: attach stream ended %s after SIGTERM", attachAt.Sub(sigAt))

	// 6. The initiating send observer is drain-cancelled too. It receives a clean
	//    SSE epilogue for the first half, but not the still-gated second half.
	var completion turnCompletion
	select {
	case completion = <-sendResult:
	case <-time.After(drainFlipBudget):
		t.Fatalf("send observer did not end within %s of SIGTERM\n%s", drainFlipBudget, h.proc.LogTail(40))
	}
	if completion.err != nil || completion.text != part1 || !completion.sawFinish || !completion.sawDone {
		t.Fatalf("send observer did not detach cleanly: err=%v text=%q want=%q finish=%t done=%t frames=%v\n%s",
			completion.err, completion.text, part1, completion.sawFinish, completion.sawDone, completion.types, h.proc.LogTail(40))
	}

	// 7. Release the pinned turn. HTTP has already drained, so accepted-work drain
	//    must now wait for the detached producer to persist the complete reply.
	gate.Release()
	want := part1 + part2

	// 8. The process must exit 0 once the drain completes. This is the reliable
	//    proof the graceful shutdown finished. The cleanup stop() then takes its
	//    already-exited branch and stays a no-op.
	select {
	case <-h.proc.Done():
		if h.proc.WaitErr() != nil {
			t.Fatalf("server exited non-zero after graceful drain: %v\n%s", h.proc.WaitErr(), h.proc.LogTail(40))
		}
	case <-time.After(gracefulTimeout):
		t.Fatalf("server did not exit within %s of the drained turn completing\n%s", gracefulTimeout, h.proc.LogTail(40))
	}
	t.Logf("graceful_drain: server exited 0 %s after SIGTERM", time.Since(sigAt))
	h.assertChatRowsPersisted(t, ctx, sessionID, agentID, "ping "+h.runID, want)
}

// createDrainProvider registers the run-scoped Anthropic provider for the drain
// journey, distinct from the other chat journeys' providers.
func (h *harness) createDrainProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	id := "anthropic-drain-" + h.runID
	body := map[string]any{
		"id":       id,
		"type":     "anthropic",
		"name":     id,
		"enabled":  true,
		"api_key":  "system-test-not-a-secret",
		"base_url": baseURL,
	}
	resp := h.postJSON(t, ctx, "/api/providers", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/providers (drain provider) = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.LogTail(40))
	}
	return id
}

// turnCompletion is the outcome of reading the send turn's SSE stream to its end.
type turnCompletion struct {
	text      string
	types     []string
	sawFinish bool
	sawDone   bool
	err       error
}

// runGatedSendTurn sends a message and reads the SSE stream to completion. It
// signals firstDelta once (when the first non-empty text-delta arrives, proving
// the turn is live) and sends the assembled result when the stream ends. All
// failures are returned on the result channel rather than failing the test from
// this goroutine, so the main goroutine owns assertion ordering.
func (h *harness) runGatedSendTurn(t *testing.T, ctx context.Context, agentID, sessionID string, firstDelta chan<- struct{}, out chan<- turnCompletion) {
	body := map[string]any{"parts": []map[string]any{{"type": "text", "text": "ping " + h.runID}}}
	payload, err := json.Marshal(body)
	if err != nil {
		out <- turnCompletion{err: fmt.Errorf("marshal send body: %w", err)}
		return
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		out <- turnCompletion{err: fmt.Errorf("build send request: %w", err)}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.client.Do(req)
	if err != nil {
		out <- turnCompletion{err: fmt.Errorf("POST send message: %w", err)}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		out <- turnCompletion{err: fmt.Errorf("send message status %d, want 200", resp.StatusCode)}
		return
	}

	var (
		result turnCompletion
		text   strings.Builder
		fired  bool
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			result.sawDone = true
			break
		}
		var evt turnEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			out <- turnCompletion{err: fmt.Errorf("invalid SSE frame %q: %w", data, err)}
			return
		}
		result.types = append(result.types, evt.Type)
		switch evt.Type {
		case "text-delta":
			text.WriteString(evt.Delta)
			if !fired && evt.Delta != "" {
				fired = true
				firstDelta <- struct{}{}
			}
		case "finish":
			result.sawFinish = true
		case "error":
			out <- turnCompletion{err: fmt.Errorf("turn emitted error frame: %q", evt.ErrorText)}
			return
		}
	}
	result.text = text.String()
	if err := scanner.Err(); err != nil {
		result.err = fmt.Errorf("read SSE stream after frames %v (text=%q): %w", result.types, result.text, err)
	}
	out <- result
}

// runAttachStream opens the read-only attach subscription for the session's
// in-flight turn and reads it until it ends, reporting the end time. It requires
// a 200 (the turn is live); a 204 would mean the server saw no in-flight turn,
// which fails the premise of the drain-cancel assertion.
func (h *harness) runAttachStream(t *testing.T, ctx context.Context, agentID, sessionID string, ended chan<- time.Time) {
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/events", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Errorf("build attach request: %v", err)
		ended <- time.Now()
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Errorf("GET attach stream: %v", err)
		ended <- time.Now()
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("attach stream = %d, want 200 (an in-flight turn must be attachable, not 204)", resp.StatusCode)
		ended <- time.Now()
		return
	}
	// Drain to end: the stream closes when drain cancels its context.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok && data == "[DONE]" {
			break
		}
	}
	ended <- time.Now()
}

// readyzStatus probes /readyz once with a short deadline and returns the status.
func (h *harness) readyzStatus(t *testing.T, ctx context.Context) int {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, h.baseURL+"/readyz", nil)
	if err != nil {
		t.Fatalf("build readyz request: %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// readyzFlip records the first not-ready observation after drain begins.
type readyzFlip struct {
	status int   // 503 when the drain flip was observed on the wire; 0 on a connection error
	err    error // non-nil when the listener had already closed
	at     time.Time
}

// sampleReadyzUntilNotReady tight-loops /readyz on a hot keep-alive connection
// and reports the first non-ready observation — a 503 (draining) or a connection
// error (listener closed) — then returns. Sampling on a reused connection with no
// idle gap maximizes the chance of catching the 503 in the narrow window between
// the drain flag flipping and the listener closing. It stops if signalled first.
func sampleReadyzUntilNotReady(baseURL string, stop <-chan struct{}, out chan<- readyzFlip) {
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	for {
		select {
		case <-stop:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			cancel()
			out <- readyzFlip{err: err, at: time.Now()}
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			out <- readyzFlip{err: err, at: time.Now()}
			return
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		cancel()
		if status != http.StatusOK {
			out <- readyzFlip{status: status, at: time.Now()}
			return
		}
	}
}
