//go:build system

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The tool_smoke journey calls every model-visible builtin tool once, over the
// wire, through the exact path a model uses: Code Mode. The fake provider
// scripts one `code` call per tool, the VM runs tools.invoke against the real
// registry, and the assertion reads the child tool's own result off the SSE
// stream rather than trusting what the JavaScript chose to return.
//
// Why a system test and not a unit test: a tool is only "callable" once the
// daemon has constructed its service, its availability predicate has passed for
// a real authenticated user, and its schema has survived the provider round
// trip. Nothing below the subprocess layer can prove that, which is exactly why
// 51 generated tools plus the hand-written core shipped six action-split PRs
// with no end-to-end call behind them.
//
// The coverage list is closed in both directions (see
// assertSmokeCoverageIsClosed): a catalog entry with no case fails, and a case
// naming a tool the catalog does not offer fails. Adding a tool means adding a
// case.

// smokeState carries values one case discovers into the cases that need them,
// so a sibling tool verifies the side effect its family member committed
// (vault_secret_set -> vault_secret_list) instead of a case inventing an id.
type smokeState struct {
	values map[string]string
}

func (s *smokeState) set(key, value string) { s.values[key] = value }

// need returns a value an earlier case captured. A missing key means the
// producing case failed, so the dependent case fails with that reason rather
// than invoking its tool with a nonsense argument.
func (s *smokeState) need(t *testing.T, key string) string {
	t.Helper()
	value := s.values[key]
	if value == "" {
		t.Fatalf("tool smoke: %q was never captured; the case that produces it did not succeed", key)
	}
	return value
}

// smokeCase is one `code` call. It invokes its subject tool once, and — only
// where a side effect must be retired inside the same VM call — the tools named
// in covers as well.
type smokeCase struct {
	// tool is the subject: the model-facing name, the subtest name, and the key
	// the coverage closure counts.
	tool string
	// args builds the subject's input, possibly from earlier captured state.
	args func(t *testing.T, s *smokeState) map[string]any
	// script replaces the generated single-invoke program. Use it only when one
	// VM call must invoke more than one tool; covers must then name the rest.
	script func(t *testing.T, s *smokeState) string
	// covers names the further tools this case's script invokes. They count as
	// covered and their results are asserted like the subject's.
	covers []string
	// check validates the case's results, keyed by tool name. A nil check
	// accepts any non-error result: the tool ran, returned, and decoded.
	check func(t *testing.T, s *smokeState, results map[string]string)
	// assertsErrorShapeOnly names the canonical error a tool must return when its
	// success precondition cannot be produced in a test deployment. The pattern
	// is matched against the error text. These cases prove the error contract,
	// not the success path, and the coverage report lists them separately.
	assertsErrorShapeOnly string
	// skip records why a tool this build defines is not invoked here. A skipped
	// tool must also be absent from the catalog, so a skip can never hide a
	// regression in a tool the model can actually reach.
	skip string
}

// pendingTools is scaffolding: catalog entries whose case is not written yet. It
// exists only so the closure assertion can land before the last family does, and
// it MUST be empty when this branch merges.
var pendingTools = []string{
	"oauth_connect", "oauth_disconnect", "oauth_flow_status", "oauth_list",
	"share_create_article", "share_create_artifact", "share_list", "share_revoke",
	"recally_article_get", "recally_article_list", "recally_article_save",
	"recally_digest_get", "recally_digest_save",
	"recally_entry_add", "recally_entry_list", "recally_entry_update",
	"recally_feed_add", "recally_feed_list", "recally_feed_poll", "recally_feed_remove",
	"session_create", "session_get", "session_list", "session_send",
	"skill_installed_search", "skill_load",
	"library_search", "memory_read", "memory_search",
	"view_image", "notify",
}

// smokeCases is the ordered case list. Order matters inside a family: a create
// runs before the get that reads it and the delete that retires it.
func smokeCases() []smokeCase {
	var cases []smokeCase
	cases = append(cases, coreSmokeCases()...)
	cases = append(cases, schedulerSmokeCases()...)
	cases = append(cases, vaultSmokeCases()...)
	cases = append(cases, goalSmokeCases()...)
	cases = append(cases, workflowSmokeCases()...)
	cases = append(cases, offCatalogSmokeCases()...)
	return cases
}

// offCatalogSmokeCases records the tools this deployment's chat catalog does not
// offer, with the reason. Each is asserted absent from the catalog.
func offCatalogSmokeCases() []smokeCase {
	return []smokeCase{{
		tool: "goal_control",
		skip: "registered only inside a Goal attempt's executor, never in a chat session; goal_lifecycle is the journey that drives it",
	}}
}

func coreSmokeCases() []smokeCase {
	return []smokeCase{{
		tool: "bash",
		args: func(t *testing.T, s *smokeState) map[string]any {
			return map[string]any{"command": "echo tool-smoke-bash-" + s.values["runID"]}
		},
		check: func(t *testing.T, s *smokeState, results map[string]string) {
			if !strings.Contains(results["bash"], "tool-smoke-bash-"+s.values["runID"]) {
				t.Errorf("bash output = %q, want the echoed run-scoped marker", results["bash"])
			}
		},
	}}
}

func schedulerSmokeCases() []smokeCase {
	const jobName = "tool-smoke-job"
	return []smokeCase{
		{
			tool: "scheduler_job_create",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{
					"name":            jobName + "-" + s.values["runID"],
					"message":         "tool smoke scheduled message",
					"cron":            "0 4 * * *",
					"enabled":         true,
					"idempotency_key": "tool-smoke-" + s.values["runID"],
				}
			},
			check: captureID("scheduler_job_create", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_get",
			args:  byID("scheduler_job_id"),
			check: expectSameID("scheduler_job_get", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_list",
			args:  noArgs,
			check: expectMentions("scheduler_job_list", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_pause",
			args:  byID("scheduler_job_id"),
			check: expectSameID("scheduler_job_pause", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_resume",
			args:  byID("scheduler_job_id"),
			check: expectSameID("scheduler_job_resume", "scheduler_job_id"),
		},
		{
			tool: "scheduler_job_update",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"id": s.need(t, "scheduler_job_id"), "message": "tool smoke updated message"}
			},
			check: expectSameID("scheduler_job_update", "scheduler_job_id"),
		},
		{
			tool: "scheduler_job_delete",
			args: byID("scheduler_job_id"),
		},
	}
}

func vaultSmokeCases() []smokeCase {
	const secretName = "TOOL_SMOKE_SECRET"
	return []smokeCase{
		{
			tool: "vault_secret_set",
			args: func(t *testing.T, s *smokeState) map[string]any {
				// A fixture value, never a real credential. The check below is what
				// proves the tool does not echo it back into the transcript.
				return map[string]any{"name": secretName, "scope": "user", "value": "tool-smoke-not-a-secret"}
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				if strings.Contains(results["vault_secret_set"], "tool-smoke-not-a-secret") {
					t.Errorf("vault_secret_set echoed the secret value into the model transcript: %q", results["vault_secret_set"])
				}
				s.set("vault_secret_name", secretName)
			},
		},
		{
			tool:  "vault_secret_list",
			args:  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"scope": "user"} },
			check: expectMentions("vault_secret_list", "vault_secret_name"),
		},
		{
			tool: "vault_secret_delete",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"name": s.need(t, "vault_secret_name"), "scope": "user"}
			},
		},
	}
}

// goalSmokeCases creates one goal and reads it back. goal_create and
// goal_cancel share a single VM call on purpose: the tool always creates a
// draft composite, and the Goal dispatcher claims a draft composite for
// autonomous decomposition on its next 2s tick. Those planner turns are
// asynchronous model calls on this same agent, and — since Code Mode moved
// goal_control off the provider-facing tool list — they are no longer
// distinguishable from a chat turn, so the fake cannot answer them without
// stealing this journey's scripted responses. Retiring the goal microseconds
// after it is created is what keeps the dispatcher out of this journey. See the
// note on workflowSmokeCases.
func goalSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool:   "goal_create",
			covers: []string{"goal_cancel"},
			script: func(t *testing.T, s *smokeState) string {
				create := mustJSON(t, map[string]any{
					"title":           "tool smoke goal " + s.values["runID"],
					"intent":          "exist long enough for the goal family to read it",
					"review_policy":   "none",
					"idempotency_key": "tool-smoke-goal-" + s.values["runID"],
				})
				return fmt.Sprintf(
					"const created = await tools.invoke(\"goal_create\", %s);\n"+
						"const id = tools.json(created).id;\n"+
						"await tools.invoke(\"goal_cancel\", { id: id, reason: \"tool smoke retires its own goal\" });\n"+
						"return id;",
					create)
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				s.set("goal_id", requireJSONString(t, "goal_create", results["goal_create"], "id"))
				if got := requireJSONString(t, "goal_cancel", results["goal_cancel"], "id"); got != s.values["goal_id"] {
					t.Errorf("goal_cancel retired %q, want the goal goal_create returned %q", got, s.values["goal_id"])
				}
				if !strings.Contains(results["goal_cancel"], "cancelled") {
					t.Errorf("goal_cancel result does not report a cancelled goal: %s", truncate(results["goal_cancel"], 800))
				}
			},
		},
		{
			tool:  "goal_get",
			args:  byID("goal_id"),
			check: expectSameID("goal_get", "goal_id"),
		},
		{
			tool:  "goal_list",
			args:  noArgs,
			check: expectMentions("goal_list", "goal_id"),
		},
	}
}

// workflowSmokeCases is the journey's one incomplete family, and the reason is
// worth stating rather than hiding behind a nil check. workflow_save requires a
// composite root in done/accepted, a state only the Goal dispatcher's planner
// and executor attempts can produce. Those attempts are model turns that the
// fake can no longer identify: Code Mode keeps goal_control out of the
// provider-facing tool list, so the advertised-action discriminator the fake
// scripts Goal runs with sees nothing (the same regression that leaves the
// goal_lifecycle journey red on main). Until that is fixed, save/get/run assert
// their canonical precondition errors and only workflow_list runs its success
// path.
func workflowSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "workflow_save",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"goal_id": s.need(t, "goal_id"), "name": "tool-smoke-workflow-" + s.values["runID"]}
			},
			assertsErrorShapeOnly: `(?i)invalid lifecycle transition`,
		},
		{
			tool:                  "workflow_get",
			args:                  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"id": absentUUID} },
			assertsErrorShapeOnly: `(?i)(not found|no rows)`,
		},
		{
			tool:  "workflow_list",
			args:  noArgs,
			check: expectJSONObject("workflow_list"),
		},
		{
			tool: "workflow_run",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"id": absentUUID, "idempotency_key": "tool-smoke-run-" + s.values["runID"]}
			},
			assertsErrorShapeOnly: `(?i)(not found|no rows)`,
		},
	}
}

// absentUUID is a well-formed identifier that no fixture creates, so a lookup
// tool is exercised on its real not-found path rather than on a parse error.
const absentUUID = "00000000-0000-4000-8000-000000000000"

func noArgs(t *testing.T, s *smokeState) map[string]any { return map[string]any{} }

func byID(key string) func(*testing.T, *smokeState) map[string]any {
	return func(t *testing.T, s *smokeState) map[string]any {
		return map[string]any{"id": s.need(t, key)}
	}
}

func captureID(tool, key string) func(*testing.T, *smokeState, map[string]string) {
	return func(t *testing.T, s *smokeState, results map[string]string) {
		s.set(key, requireJSONString(t, tool, results[tool], "id"))
	}
}

// expectSameID proves a sibling tool answered about the object the producing
// case created, not merely that it returned something.
func expectSameID(tool, key string) func(*testing.T, *smokeState, map[string]string) {
	return func(t *testing.T, s *smokeState, results map[string]string) {
		if got := requireJSONString(t, tool, results[tool], "id"); got != s.need(t, key) {
			t.Errorf("%s returned id %q, want the captured %s %q", tool, got, key, s.values[key])
		}
	}
}

func expectMentions(tool, key string) func(*testing.T, *smokeState, map[string]string) {
	return func(t *testing.T, s *smokeState, results map[string]string) {
		want := s.need(t, key)
		if !strings.Contains(results[tool], want) {
			t.Errorf("%s does not mention %s %q: %s", tool, key, want, truncate(results[tool], 800))
		}
	}
}

// expectJSONObject is the weakest useful contract: the tool answered with a
// decodable JSON object rather than prose or an empty body.
func expectJSONObject(tool string) func(*testing.T, *smokeState, map[string]string) {
	return func(t *testing.T, s *smokeState, results map[string]string) {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(results[tool]), &decoded); err != nil {
			t.Errorf("%s result is not a JSON object: %v\n%s", tool, err, truncate(results[tool], 800))
		}
	}
}

// requireJSONString decodes one string field out of a tool result. A tool whose
// result is not JSON, or lacks the field, has broken its output contract.
func requireJSONString(t *testing.T, tool, output, field string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("%s result is not a JSON object: %v\n%s", tool, err, truncate(output, 800))
	}
	value, _ := decoded[field].(string)
	if value == "" {
		t.Fatalf("%s result has no non-empty %q field: %s", tool, field, truncate(output, 800))
	}
	return value
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// testToolSmoke is the journey entry point.
func (h *harness) testToolSmoke(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-tool-smoke-"+h.runID)
	agentID := h.createAgentNamed(t, ctx, providerID+"/"+modelID, "tool-smoke-agent-"+h.runID)
	sessionID := h.createSession(t, ctx, agentID)

	cases := smokeCases()
	catalog := h.readCodeCatalog(t, ctx, fake, agentID, sessionID)
	assertSmokeCoverageIsClosed(t, catalog, cases)

	state := &smokeState{values: map[string]string{"runID": h.runID}}
	report := make([]string, 0, len(cases))
	for _, smoke := range cases {
		if smoke.skip != "" {
			report = append(report, fmt.Sprintf("%-26s skipped           %s", smoke.tool, smoke.skip))
			continue
		}
		outcome := "ok"
		if smoke.assertsErrorShapeOnly != "" {
			outcome = "error-shape-only"
		}
		if !t.Run(smoke.tool, func(t *testing.T) {
			h.runSmokeCase(t, ctx, fake, agentID, sessionID, smoke, state)
		}) {
			outcome = "FAILED"
		}
		report = append(report, fmt.Sprintf("%-26s %s", smoke.tool, outcome))
	}
	t.Logf("tool smoke coverage (%d cases):\n%s", len(report), strings.Join(report, "\n"))
}

// runSmokeCase drives one turn: the model calls `code`, the VM invokes the
// case's tools, and each child's own result comes back on the SSE stream.
func (h *harness) runSmokeCase(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID, sessionID string, smoke smokeCase, state *smokeState) {
	t.Helper()
	callID := "toolu_smoke_" + smoke.tool
	fake.enqueueTool(callID, "code", h.smokeCodeArgs(t, smoke, state))
	fake.enqueueText("smoke " + smoke.tool + " done")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "smoke " + smoke.tool},
	})

	settled := childToolResults(events)
	results := make(map[string]string, len(smoke.covers)+1)
	for _, tool := range append([]string{smoke.tool}, smoke.covers...) {
		child, ok := settled[tool]
		if !ok {
			t.Fatalf("no child tool result for %q; the code call never reached it. frames: %v\n%s",
				tool, eventTypes(events), h.proc.logTail(60))
		}
		t.Logf("%s result: %s", tool, truncate(child.text, 400))
		if tool == smoke.tool && smoke.assertsErrorShapeOnly != "" {
			if !child.failed {
				t.Fatalf("%s succeeded, but the case only asserts its canonical error; drop assertsErrorShapeOnly: %s",
					tool, truncate(child.text, 800))
			}
			if !regexp.MustCompile(smoke.assertsErrorShapeOnly).MatchString(child.text) {
				t.Fatalf("%s error = %q, want a match for %q", tool, truncate(child.text, 800), smoke.assertsErrorShapeOnly)
			}
			return
		}
		if child.failed {
			t.Fatalf("%s returned an error result: %s\n%s", tool, truncate(child.text, 2000), h.proc.logTail(60))
		}
		results[tool] = child.text
	}
	if smoke.check != nil {
		smoke.check(t, state, results)
	}
}

// smokeCodeArgs renders the `code` tool input for one case. The generated
// single-invoke program settles the rejection with an explicit onRejected
// handler rather than try/catch around await: the Code Mode VM marks a child
// failure observed only through the promise's own then/catch, so an awaited
// rejection would still be rethrown when the executor drains its children.
func (h *harness) smokeCodeArgs(t *testing.T, smoke smokeCase, state *smokeState) string {
	t.Helper()
	var script string
	switch {
	case smoke.script != nil:
		script = smoke.script(t, state)
	default:
		args := map[string]any{}
		if smoke.args != nil {
			args = smoke.args(t, state)
		}
		script = fmt.Sprintf(
			"return tools.invoke(%s, %s).then("+
				"result => ({ ok: true, text: tools.text(result) }),"+
				"failure => ({ ok: false, error: failure && failure.value ? tools.text(failure.value) : String(failure && failure.message) })"+
				");",
			mustJSON(t, smoke.tool), mustJSON(t, args))
	}
	return mustJSON(t, map[string]string{"code": script})
}

// childToolResult is one settled child invocation observed on the SSE stream.
type childToolResult struct {
	text   string
	failed bool
}

// childToolResults pairs each tool-input-start frame, which is the only frame
// carrying the tool name, with the settled frame carrying that call's result.
func childToolResults(events []turnEvent) map[string]childToolResult {
	names := map[string]string{}
	settled := map[string]childToolResult{}
	for _, event := range events {
		switch event.Type {
		case "tool-input-start":
			names[event.ToolCallID] = event.ToolName
		case "tool-output-available":
			if name, ok := names[event.ToolCallID]; ok {
				settled[name] = childToolResult{text: event.Output}
			}
		case "tool-output-error":
			if name, ok := names[event.ToolCallID]; ok {
				settled[name] = childToolResult{text: event.ErrorText, failed: true}
			}
		}
	}
	return settled
}

// readCodeCatalog asks the running system what tools the model can reach. The
// catalog has no Go-side accessor by design, so the journey pages it out
// through the same tools.search the model uses.
func (h *harness) readCodeCatalog(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID, sessionID string) []string {
	t.Helper()
	const listCatalog = `{"code":"const names = []; for (let offset = 0; ; ) { const page = tools.search(\"\", offset); for (const tool of page) { names.push(tool.name); } if (!page.hasMore) { break; } offset = page.nextOffset; } return names;"}`
	fake.enqueueTool("toolu_smoke_catalog", "code", listCatalog)
	fake.enqueueText("catalog listed")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "list the tool catalog"},
	})
	var raw string
	for _, event := range events {
		if event.Type == "tool-output-available" && event.ToolCallID == "toolu_smoke_catalog" {
			raw = event.Output
		}
	}
	if raw == "" {
		t.Fatalf("the code catalog call produced no output; frames: %v\n%s", eventTypes(events), h.proc.logTail(60))
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		t.Fatalf("code catalog is not a JSON array: %v\n%s", err, truncate(raw, 800))
	}
	sort.Strings(names)
	t.Logf("code catalog (%d tools): %s", len(names), strings.Join(names, " "))
	return names
}

// assertSmokeCoverageIsClosed is the discipline this journey exists for. The
// catalog is the model-visible surface, and it must equal the set of tools the
// cases invoke: an uncovered catalog entry is an untested tool, and a case for a
// tool the catalog does not offer is a case that proves nothing.
func assertSmokeCoverageIsClosed(t *testing.T, catalog []string, cases []smokeCase) {
	t.Helper()
	covered := map[string]smokeCase{}
	for _, smoke := range cases {
		for _, tool := range append([]string{smoke.tool}, smoke.covers...) {
			if _, duplicate := covered[tool]; duplicate {
				t.Errorf("tool smoke: %q is invoked by more than one case; a tool is invoked exactly once", tool)
			}
			covered[tool] = smoke
		}
	}
	inCatalog := map[string]bool{}
	for _, name := range catalog {
		inCatalog[name] = true
	}

	for _, name := range catalog {
		smoke, ok := covered[name]
		switch {
		case !ok && slices.Contains(pendingTools, name):
			t.Logf("tool smoke: %q is pending a case (see pendingTools)", name)
		case !ok:
			t.Errorf("tool smoke: %q is in the model's catalog but has no smoke case; add one to smokeCases()", name)
		case smoke.skip != "":
			t.Errorf("tool smoke: %q is skipped as %q, but the catalog offers it to the model; write a real case", name, smoke.skip)
		}
	}
	for tool, smoke := range covered {
		if smoke.skip == "" && !inCatalog[tool] {
			t.Errorf("tool smoke: %q has a case but the catalog does not offer it; the case proves nothing (catalog: %s)",
				tool, strings.Join(catalog, " "))
		}
	}
	// The code tool is the entry point, never a catalog entry: every case above
	// reaches its tool through it.
	if inCatalog["code"] {
		t.Error("tool smoke: `code` is inside its own catalog; the entry point must not be reachable as a child call")
	}
}
