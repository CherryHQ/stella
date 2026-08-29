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
// Why a system test and not a unit test: a tool is only "callable" once its
// service is constructed by the daemon, its availability predicate has passed
// for a real authenticated user, and its schema has survived the provider
// round trip. Nothing below the subprocess layer can prove that, which is
// exactly why 51 generated tools plus the hand-written core shipped six action-
// split PRs with no end-to-end call behind them.
//
// The list is closed in both directions (see assertSmokeCoverageIsClosed): a
// catalog entry with no case fails, and a case naming a tool the catalog does
// not offer fails. Adding a tool therefore means adding a case.

// smokeState carries values one case discovers into the cases that need them,
// so a sibling tool verifies the side effect its family member committed
// (vault_secret_set -> vault_secret_list) instead of a case inventing an id.
type smokeState struct {
	values map[string]string
}

func (s *smokeState) set(key, value string) { s.values[key] = value }

// need returns a value an earlier case captured. A missing key means the
// producing case failed, so the dependent case is skipped with that reason
// rather than invoking the tool with a nonsense argument.
func (s *smokeState) need(t *testing.T, key string) string {
	t.Helper()
	value := s.values[key]
	if value == "" {
		t.Fatalf("tool smoke: %q was never captured; the case that produces it did not succeed", key)
	}
	return value
}

// smokeCase is one tool's single invocation and the contract its result must
// satisfy. Exactly one of check / assertsErrorShapeOnly / skip applies.
type smokeCase struct {
	// tool is the model-facing tool name, and the key the coverage closure uses.
	tool string
	// args builds the invocation input, possibly from earlier captured state.
	args func(t *testing.T, s *smokeState) map[string]any
	// check validates the tool's own result text. A nil check accepts any
	// non-error result: the tool ran, returned, and decoded.
	check func(t *testing.T, s *smokeState, output string)
	// assertsErrorShapeOnly names the canonical error a tool must return when
	// its environment cannot be satisfied deterministically in a test
	// deployment. The pattern is matched against the error text. Cases carrying
	// it are listed separately in the coverage report: they prove the error
	// contract, not the success path.
	assertsErrorShapeOnly string
	// skip records why a tool the build defines is not invoked here. It must
	// also be absent from the catalog, so a skip cannot hide a regression in a
	// tool the model can actually reach.
	skip string
}

// pendingTools is scaffolding: tools whose case is not written yet. It exists
// only so the closure assertion can land before the last family does, and it
// MUST be empty when this branch merges.
var pendingTools = []string{
	"goal_cancel", "goal_create", "goal_get", "goal_list",
	"workflow_get", "workflow_list", "workflow_run", "workflow_save",
	"oauth_connect", "oauth_disconnect", "oauth_flow_status", "oauth_list",
	"email_account_list", "email_message_list", "email_message_read", "email_message_send",
	"share_create_article", "share_create_artifact", "share_list", "share_revoke",
	"recally_article_get", "recally_article_list", "recally_article_save",
	"recally_digest_get", "recally_digest_save",
	"recally_entry_add", "recally_entry_list", "recally_entry_update",
	"recally_feed_add", "recally_feed_list", "recally_feed_poll", "recally_feed_remove",
	"session_create", "session_get", "session_list", "session_send",
	"skill_installed_search", "skill_load",
	"library_search", "memory_read", "memory_search",
	"view_image", "notify", "webfetch",
}

// smokeCases is the ordered case list. Order matters inside a family: a create
// runs before the get that reads it and the delete that retires it.
func smokeCases() []smokeCase {
	var cases []smokeCase
	cases = append(cases, coreSmokeCases()...)
	cases = append(cases, schedulerSmokeCases()...)
	cases = append(cases, vaultSmokeCases()...)
	cases = append(cases, offCatalogSmokeCases()...)
	return cases
}

// offCatalogSmokeCases records the tools this deployment's chat catalog does
// not offer, with the reason. They are asserted absent from the catalog.
func offCatalogSmokeCases() []smokeCase {
	return []smokeCase{{
		tool: "goal_control",
		skip: "registered only inside a Goal attempt's executor, never in a chat session; the goal_lifecycle journey drives it end to end",
	}}
}

func coreSmokeCases() []smokeCase {
	return []smokeCase{{
		tool: "bash",
		args: func(t *testing.T, s *smokeState) map[string]any {
			return map[string]any{"command": "echo tool-smoke-bash-" + s.values["runID"]}
		},
		check: func(t *testing.T, s *smokeState, output string) {
			if !strings.Contains(output, "tool-smoke-bash-"+s.values["runID"]) {
				t.Errorf("bash output = %q, want the echoed run-scoped marker", output)
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
			check: func(t *testing.T, s *smokeState, output string) {
				s.set("scheduler_job_id", requireJSONString(t, "scheduler_job_create", output, "id"))
			},
		},
		{
			tool:  "scheduler_job_get",
			args:  byID("scheduler_job_id"),
			check: expectJSONFieldEquals("id", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_list",
			args:  noArgs,
			check: expectContainsCaptured("scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_pause",
			args:  byID("scheduler_job_id"),
			check: expectJSONFieldEquals("id", "scheduler_job_id"),
		},
		{
			tool:  "scheduler_job_resume",
			args:  byID("scheduler_job_id"),
			check: expectJSONFieldEquals("id", "scheduler_job_id"),
		},
		{
			tool: "scheduler_job_update",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"id": s.need(t, "scheduler_job_id"), "message": "tool smoke updated message"}
			},
			check: expectJSONFieldEquals("id", "scheduler_job_id"),
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
				// A fixture value, never a real credential. The tool must not echo
				// it back, which the check below is what proves.
				return map[string]any{"name": secretName, "scope": "user", "value": "tool-smoke-not-a-secret"}
			},
			check: func(t *testing.T, s *smokeState, output string) {
				if strings.Contains(output, "tool-smoke-not-a-secret") {
					t.Errorf("vault_secret_set echoed the secret value back into the model transcript: %q", output)
				}
				s.set("vault_secret_name", secretName)
			},
		},
		{
			tool:  "vault_secret_list",
			args:  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"scope": "user"} },
			check: expectContainsCaptured("vault_secret_name"),
		},
		{
			tool: "vault_secret_delete",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"name": s.need(t, "vault_secret_name"), "scope": "user"}
			},
		},
	}
}

func noArgs(t *testing.T, s *smokeState) map[string]any { return map[string]any{} }

func byID(key string) func(*testing.T, *smokeState) map[string]any {
	return func(t *testing.T, s *smokeState) map[string]any {
		return map[string]any{"id": s.need(t, key)}
	}
}

// expectJSONFieldEquals proves a sibling tool answered about the same object
// the producing case created, not merely that it returned something.
func expectJSONFieldEquals(field, key string) func(*testing.T, *smokeState, string) {
	return func(t *testing.T, s *smokeState, output string) {
		if got := requireJSONString(t, field, output, field); got != s.need(t, key) {
			t.Errorf("result %s = %q, want the captured %s %q", field, got, key, s.values[key])
		}
	}
}

func expectContainsCaptured(key string) func(*testing.T, *smokeState, string) {
	return func(t *testing.T, s *smokeState, output string) {
		want := s.need(t, key)
		if !strings.Contains(output, want) {
			t.Errorf("result does not mention %s %q: %s", key, want, truncate(output, 800))
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
			report = append(report, fmt.Sprintf("%-26s skipped  (%s)", smoke.tool, smoke.skip))
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

// runSmokeCase drives one turn: the model calls `code`, the VM invokes exactly
// one tool, and the child's own result comes back on the SSE stream.
func (h *harness) runSmokeCase(t *testing.T, ctx context.Context, fake *fakeAnthropic, agentID, sessionID string, smoke smokeCase, state *smokeState) {
	t.Helper()
	args := map[string]any{}
	if smoke.args != nil {
		args = smoke.args(t, state)
	}
	callID := "toolu_smoke_" + smoke.tool
	fake.enqueueTool(callID, "code", codeInvokeArgs(t, smoke.tool, args))
	fake.enqueueText("smoke " + smoke.tool + " done")

	events, _ := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "text", "text": "smoke " + smoke.tool},
	})

	child, ok := findChildToolResult(events, smoke.tool)
	if !ok {
		t.Fatalf("no child tool result for %q; the code call never reached the tool. frames: %v\n%s",
			smoke.tool, eventTypes(events), h.proc.logTail(60))
	}
	if smoke.assertsErrorShapeOnly != "" {
		if !child.failed {
			t.Fatalf("%s succeeded, but the case only asserts its canonical error; drop assertsErrorShapeOnly: %s",
				smoke.tool, truncate(child.text, 800))
		}
		if !regexp.MustCompile(smoke.assertsErrorShapeOnly).MatchString(child.text) {
			t.Fatalf("%s error = %q, want a match for %q", smoke.tool, truncate(child.text, 800), smoke.assertsErrorShapeOnly)
		}
		return
	}
	if child.failed {
		t.Fatalf("%s returned an error result: %s\n%s", smoke.tool, truncate(child.text, 2000), h.proc.logTail(60))
	}
	if smoke.check != nil {
		smoke.check(t, state, child.text)
	}
}

// codeInvokeArgs renders the `code` tool input for one invocation. The script
// settles the rejection with an explicit onRejected handler rather than
// try/catch around await: the Code Mode VM marks a child failure observed only
// through the promise's own then/catch, so an awaited rejection would still be
// rethrown when the executor drains its children.
func codeInvokeArgs(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", tool, err)
	}
	encodedTool, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal %s name: %v", tool, err)
	}
	script := fmt.Sprintf(
		"return tools.invoke(%s, %s).then("+
			"result => ({ ok: true, text: tools.text(result) }),"+
			"failure => ({ ok: false, error: failure && failure.value ? tools.text(failure.value) : String(failure && failure.message) })"+
			");",
		encodedTool, encodedArgs)
	payload, err := json.Marshal(map[string]string{"code": script})
	if err != nil {
		t.Fatalf("marshal code arguments: %v", err)
	}
	return string(payload)
}

// childToolResult is one settled child invocation observed on the SSE stream.
type childToolResult struct {
	text   string
	failed bool
}

// findChildToolResult pairs the tool-input-start frame that names the tool with
// the settled frame carrying its result. The output frame carries only the call
// id, so the name has to come from the start frame.
func findChildToolResult(events []turnEvent, tool string) (childToolResult, bool) {
	var callID string
	for _, event := range events {
		if event.Type == "tool-input-start" && event.ToolName == tool {
			callID = event.ToolCallID
			continue
		}
		if callID == "" || event.ToolCallID != callID {
			continue
		}
		switch event.Type {
		case "tool-output-available":
			return childToolResult{text: event.Output}, true
		case "tool-output-error":
			return childToolResult{text: event.ErrorText, failed: true}, true
		}
	}
	return childToolResult{}, false
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
// catalog is the model-visible surface, and it must equal the set of cases that
// invoke a tool: an uncovered catalog entry is an untested tool, and a case for
// a tool the catalog does not offer is a case that proves nothing.
func assertSmokeCoverageIsClosed(t *testing.T, catalog []string, cases []smokeCase) {
	t.Helper()
	covered := make(map[string]smokeCase, len(cases))
	for _, smoke := range cases {
		if _, duplicate := covered[smoke.tool]; duplicate {
			t.Errorf("tool smoke: %q has more than one case; a tool is invoked exactly once", smoke.tool)
		}
		covered[smoke.tool] = smoke
	}
	inCatalog := make(map[string]bool, len(catalog))
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
	for _, smoke := range cases {
		if smoke.skip == "" && !inCatalog[smoke.tool] {
			t.Errorf("tool smoke: %q has a case but the catalog does not offer it; the case proves nothing (catalog: %s)",
				smoke.tool, strings.Join(catalog, " "))
		}
	}
	// The code tool is the entry point, never a catalog entry: every case above
	// reaches its tool through it.
	if inCatalog["code"] {
		t.Error("tool smoke: `code` is inside its own catalog; the entry point must not be reachable as a child call")
	}
}
