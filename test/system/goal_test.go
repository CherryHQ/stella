//go:build system

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testGoalLifecycle drives a Goal from creation to autonomous acceptance over
// the wire, with no human interaction. It proves the async seam a single
// in-process test cannot: the production binary's dispatcher (a periodic River
// job) picks up a fresh composite, runs the planner and the leaf executor as
// durable jobs against the scripted fake, folds trivial acceptance, and rolls
// the result up to the root — all while the test only ever speaks HTTP and reads
// the shared database.
//
// The traversed path is composite → decomposition → leaf execution → trivial
// auto-accept → composite rollup accept:
//
//   - A composite root is created in draft. The dispatcher's scanAndDecompose
//     mints a decomposition attempt; the planner turn calls goal_control
//     action=decompose, proposing one required leaf child.
//   - review_policy=none, so the plan materializes immediately and the child is
//     released. scanAndClaim dispatches the leaf; its execution turn calls
//     goal_control action=submit.
//   - The child carries no acceptance contract (trivial), so acceptance
//     auto-passes and the child goes done/accepted. rollupComposites then accepts
//     the required-child-complete root.
//
// Only two model turns are scripted (decompose, submit), matched by the
// goal_control action the server advertises rather than by arrival order,
// because each attempt's agent tool loop may fire a racy tool-result follow-up
// turn whose timing is not deterministic (see fake_anthropic_test.go).
func (h *harness) testGoalLifecycle(t *testing.T) {
	fake := newFakeAnthropic(t)

	// The journey's own deadline is 120s (a generous but bounded margin for the
	// dispatcher's 2s ticks plus River job latency); the context outlives it so
	// the on-timeout diagnostics queries still have budget to run.
	const pollBudget = 120 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), pollBudget+30*time.Second)
	defer cancel()

	// Script the two goal_control stages. The child carries no acceptance_contract
	// (trivial ⇒ auto-accept) and is required so its acceptance gates the root's
	// rollup.
	childKey := "leaf-" + h.runID
	fake.enqueueGoalControl("decompose", mustJSON(t, map[string]any{
		"action":  "decompose",
		"summary": "one leaf child",
		"decomposition": map[string]any{
			"children": []map[string]any{{
				"key":      childKey,
				"title":    "system-test leaf " + h.runID,
				"intent":   "produce the run marker",
				"kind":     "leaf",
				"required": true,
			}},
		},
	}))
	fake.enqueueGoalControl("submit", mustJSON(t, map[string]any{
		"action":  "submit",
		"summary": "done, run " + h.runID,
		"output":  map[string]any{"marker": h.runID},
	}))

	// A run-scoped provider/agent distinct from chat_sse's: this journey points a
	// provider at its OWN fake server, so it can neither reuse nor collide with the
	// chat provider (whose fake server is already closed).
	const modelID = "claude-sonnet-4-6"
	providerID := h.createGoalProvider(t, ctx, fake.baseURL())
	agentID := h.createGoalAgent(t, ctx, providerID+"/"+modelID)

	rootID := h.createComposite(t, ctx, agentID)

	final := h.awaitGoalAccepted(t, ctx, fake, rootID, time.Now().Add(pollBudget))

	// Terminal accepted is all three facts together: lifecycle=done alone is
	// ambiguous (failed and cancelled also land there).
	if final.Lifecycle != "done" || final.DoneReason != "accepted" || final.AcceptanceState != "passed" {
		t.Fatalf("root goal terminal state = %s/%s/%s, want done/accepted/passed\n%s",
			final.Lifecycle, final.DoneReason, final.AcceptanceState, h.dumpGoal(ctx, fake, rootID))
	}

	h.assertGoalRowsAccepted(t, ctx, rootID)
	h.assertGoalSessionsLive(t, ctx, rootID)
	assertGoalFakeRequests(t, fake)
}

// goalState is the subset of the Goal response the journey polls and asserts on.
type goalState struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Lifecycle       string `json:"lifecycle"`
	DoneReason      string `json:"done_reason"`
	AcceptanceState string `json:"acceptance_state"`
	BlockReason     string `json:"block_reason"`
}

// createGoalProvider registers a run-scoped Anthropic-type provider pointed at
// this journey's fake and returns its id. Its id carries a "-goal-" segment so it
// is independent of chat_sse's provider rather than colliding with it.
func (h *harness) createGoalProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	id := "anthropic-goal-" + h.runID
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
		t.Fatalf("POST /api/providers = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	return id
}

// createGoalAgent creates the managing/executor agent for the goal, bound to the
// given model ref, and returns its server-assigned id.
func (h *harness) createGoalAgent(t *testing.T, ctx context.Context, model string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents", map[string]any{
		"name":    "sys-test-goal-agent-" + h.runID,
		"model":   model,
		"enabled": true,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/agents = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created goal agent has empty id")
	}
	return created.ID
}

// createComposite creates a composite root goal (kind=composite,
// review_policy=none, no acceptance contract) and returns its id. A draft
// composite is auto-decomposed by the dispatcher, so no activate call is needed;
// review_policy=none makes the submitted plan materialize without a human
// approval gate.
func (h *harness) createComposite(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	return h.createCompositeTitled(t, ctx, agentID, "system-test goal "+h.runID)
}

func (h *harness) createCompositeTitled(t *testing.T, ctx context.Context, agentID, title string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/goals", map[string]any{
		"title":         title,
		"intent":        "traverse decomposition, execution, and acceptance",
		"agent_id":      agentID,
		"kind":          "composite",
		"review_policy": "none",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/goals = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created goalState
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create-goal response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created goal has empty id")
	}
	if created.Kind != "composite" {
		t.Fatalf("created goal kind = %q, want composite", created.Kind)
	}
	return created.ID
}

// awaitGoalAccepted polls GET /api/goals/{id} until the root reaches a terminal
// lifecycle, returning its final state. It fails fast on a terminal-but-not-
// accepted outcome (failed/cancelled) and on the poll deadline, attaching the
// goal tree, attempts, fake request log, and server log tail so a stuck run is
// diagnosable without a rerun.
func (h *harness) awaitGoalAccepted(t *testing.T, ctx context.Context, fake *fakeAnthropic, rootID string, deadline time.Time) goalState {
	t.Helper()
	for {
		g := h.fetchGoal(t, ctx, rootID)
		if g.Lifecycle == "done" {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("goal %s did not reach done within the deadline; last state %s/%s/%s\n%s",
				rootID, g.Lifecycle, g.DoneReason, g.AcceptanceState, h.dumpGoal(ctx, fake, rootID))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// fetchGoal reads one Goal over the API. A transport error or non-200 is a real
// defect (the goal was created, so it must be readable), so it fails rather than
// retrying.
func (h *harness) fetchGoal(t *testing.T, ctx context.Context, id string) goalState {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/goals/"+id, nil)
	if err != nil {
		t.Fatalf("build get-goal request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/goals/%s: %v\n%s", id, err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("GET /api/goals/%s = %d, want %d\n%s", id, resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var g goalState
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decode goal response: %v", err)
	}
	return g
}

// assertGoalRowsAccepted proves the subprocess persisted the terminal outcome:
// the root row is done/accepted/passed, and its required leaf child reached
// done/accepted too (the rollup's precondition, asserted directly rather than
// inferred from the root).
func (h *harness) assertGoalRowsAccepted(t *testing.T, ctx context.Context, rootID string) {
	t.Helper()

	var lifecycle, doneReason, acceptance string
	err := h.db.QueryRow(ctx,
		`SELECT lifecycle, done_reason, acceptance_state FROM agent_goal WHERE id = $1`,
		rootID).Scan(&lifecycle, &doneReason, &acceptance)
	if err != nil {
		t.Fatalf("query root goal %s: %v", rootID, err)
	}
	if lifecycle != "done" || doneReason != "accepted" || acceptance != "passed" {
		t.Errorf("root goal row = %s/%s/%s, want done/accepted/passed", lifecycle, doneReason, acceptance)
	}

	var (
		children      int
		childAccepted int
	)
	rows, err := h.db.Query(ctx,
		`SELECT lifecycle, done_reason, acceptance_state
		   FROM agent_goal
		  WHERE parent_id = $1 AND kind = 'leaf' AND required = true`,
		rootID)
	if err != nil {
		t.Fatalf("query child leaves of %s: %v", rootID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var lc, dr, as string
		if err := rows.Scan(&lc, &dr, &as); err != nil {
			t.Fatalf("scan child leaf: %v", err)
		}
		children++
		if lc == "done" && dr == "accepted" && as == "passed" {
			childAccepted++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate child leaves: %v", err)
	}
	if children == 0 {
		t.Error("root goal has no required leaf child; decomposition did not materialize the plan")
	}
	if childAccepted != children {
		t.Errorf("%d/%d required leaf children reached done/accepted", childAccepted, children)
	}
}

// assertGoalSessionsLive proves every session the run minted for its attempts
// persists un-archived. Goal attempts mint hidden sessions (KindDelegate for
// planning, KindTask for execution); a rolled-back mint archives its orphan, so
// an archived session here would signal a leaked or mis-committed attempt. A
// missing conversation row (LEFT JOIN yields NULL) fails too, catching a broken
// join key rather than passing vacuously.
func (h *harness) assertGoalSessionsLive(t *testing.T, ctx context.Context, rootID string) {
	t.Helper()
	rows, err := h.db.Query(ctx,
		`SELECT a.session_id, c.archived
		   FROM agent_goal_attempt a
		   JOIN agent_goal g ON g.id = a.goal_id
		   LEFT JOIN ctx_conversation c ON c.session_id = a.session_id
		  WHERE g.root_id = $1`,
		rootID)
	if err != nil {
		t.Fatalf("query attempt sessions for tree %s: %v", rootID, err)
	}
	defer rows.Close()
	sessions := 0
	for rows.Next() {
		var sessionID string
		var archived *bool // NULL when no conversation row matches the attempt's session
		if err := rows.Scan(&sessionID, &archived); err != nil {
			t.Fatalf("scan attempt session: %v", err)
		}
		sessions++
		switch {
		case archived == nil:
			t.Errorf("attempt session %q has no ctx_conversation row; the run-created session is missing", sessionID)
		case *archived:
			t.Errorf("attempt session %q is archived; run-created sessions must stay live", sessionID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate attempt sessions: %v", err)
	}
	// Decomposition and execution each mint a session, so an accepted run has at
	// least two — fewer means an attempt (hence a stage) never ran.
	if sessions < 2 {
		t.Errorf("run minted %d attempt session(s), want at least 2 (decomposition + execution)", sessions)
	}
}

// assertGoalFakeRequests proves the model traffic matched the script: every
// request the fake saw carried a scripted goal_control stage (no surprise
// non-goal_control or unknown-variant call), and both scripted stages were
// observed. That the two enqueued stages were consumed is enforced by the fake's
// cleanup; this adds the sequence to the test log for the record.
func assertGoalFakeRequests(t *testing.T, fake *fakeAnthropic) {
	t.Helper()
	reqs := fake.requests()
	seen := map[string]int{}
	for i, r := range reqs {
		switch r.GoalControl {
		case "decompose", "submit":
			seen[r.GoalControl]++
		default:
			t.Errorf("model request %d had goal_control=%q; want a scripted decompose/submit stage", i, r.GoalControl)
		}
	}
	if seen["decompose"] == 0 || seen["submit"] == 0 {
		t.Errorf("fake saw decompose=%d submit=%d requests, want at least one of each", seen["decompose"], seen["submit"])
	}
	t.Logf("goal journey model requests (%d): %s", len(reqs), summarizeGoalRequests(reqs))
}

// summarizeGoalRequests renders the goal_control stage of each request in
// arrival order for diagnostics, e.g. "decompose, decompose, submit".
func summarizeGoalRequests(reqs []fakeRequest) string {
	stages := make([]string, len(reqs))
	for i, r := range reqs {
		stages[i] = r.GoalControl
		if stages[i] == "" {
			stages[i] = "<none>"
		}
	}
	return strings.Join(stages, ", ")
}

// dumpGoal renders the goal tree, its attempts, the fake request log, and the
// server log tail for an on-failure diagnostic. It swallows query errors into
// the text so a diagnostic path never masks the original failure.
func (h *harness) dumpGoal(ctx context.Context, fake *fakeAnthropic, rootID string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "goal tree (root %s):\n", rootID)
	if rows, err := h.db.Query(ctx,
		`SELECT id, kind, lifecycle, done_reason, acceptance_state, block_reason
		   FROM agent_goal WHERE root_id = $1 ORDER BY depth, position`, rootID); err != nil {
		fmt.Fprintf(&b, "  <tree query error: %v>\n", err)
	} else {
		for rows.Next() {
			var id, kind, lc, dr, as, br string
			if err := rows.Scan(&id, &kind, &lc, &dr, &as, &br); err != nil {
				fmt.Fprintf(&b, "  <scan error: %v>\n", err)
				break
			}
			fmt.Fprintf(&b, "  %s kind=%s lifecycle=%s done_reason=%q acceptance=%s block=%q\n", id, kind, lc, dr, as, br)
		}
		rows.Close()
	}

	fmt.Fprintf(&b, "attempts:\n")
	if rows, err := h.db.Query(ctx,
		`SELECT a.id, a.goal_id, a.purpose, a.status, a.error
		   FROM agent_goal_attempt a JOIN agent_goal g ON g.id = a.goal_id
		  WHERE g.root_id = $1 ORDER BY a.created_at`, rootID); err != nil {
		fmt.Fprintf(&b, "  <attempts query error: %v>\n", err)
	} else {
		for rows.Next() {
			var id, goalID, purpose, status, errText string
			if err := rows.Scan(&id, &goalID, &purpose, &status, &errText); err != nil {
				fmt.Fprintf(&b, "  <scan error: %v>\n", err)
				break
			}
			fmt.Fprintf(&b, "  %s goal=%s purpose=%s status=%s error=%q\n", id, goalID, purpose, status, errText)
		}
		rows.Close()
	}

	reqs := fake.requests()
	fmt.Fprintf(&b, "fake model requests (%d): %s\n", len(reqs), summarizeGoalRequests(reqs))
	fmt.Fprintf(&b, "%s", h.proc.logTail(40))
	return b.String()
}

// mustJSON marshals v to a compact JSON string, failing the test on error. Used
// to build goal_control tool arguments from map literals.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(b)
}
