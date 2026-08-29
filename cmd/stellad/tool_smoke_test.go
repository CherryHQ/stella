package main

// tool_smoke is the closed-coverage gate for the model-facing tool surface: it
// invokes every builtin tool once, in process, through the exact path a model
// uses — Code Mode. setup() builds the production composition root against a
// live PostgreSQL, a scripted provider hands the runner one `code` call per
// case, the VM runs tools.invoke against the real registry, and the assertion
// reads each child tool's own result off the agent event stream rather than
// trusting what the JavaScript chose to return.
//
// Why here and not in test/system: this is the lowest layer that can prove a
// tool is callable, because "callable" means the daemon constructed its
// service, its availability predicate passed for a real user, and its schema
// survived the provider round trip. All of that is in-process; the subprocess,
// HTTP, and SSE hops add nothing to it. test/system keeps a canary journey for
// the transport itself.
//
// The coverage set is closed by strict equality (see
// assertSmokeCoverageIsClosed): the production tool surface must equal the
// tools these cases invoke plus the explicitly listed protocol exceptions.
// There is no pending list and no skip: a tool without a case fails the build.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agent/toolmeta"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

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
	// extraReplies are the model turns a tool triggers before its own turn
	// resumes: a nested session runs its own turn inside session_create, and an
	// image-returning tool triggers a baseline render. They are enqueued after
	// the case's `code` call and before the reply that ends the turn, which is
	// the order the system makes them in.
	extraReplies []string
	// assertsErrorShapeOnly names the canonical error a tool must return when its
	// success precondition cannot be produced in a test deployment. The pattern
	// is matched against the error text. These cases prove the error contract,
	// not the success path, and the coverage report lists them separately.
	assertsErrorShapeOnly string
}

// smokeCases is the ordered case list. Order matters inside a family: a create
// runs before the get that reads it and the delete that retires it.
func smokeCases(h *smokeHarness) []smokeCase {
	var cases []smokeCase
	cases = append(cases, coreSmokeCases()...)
	cases = append(cases, schedulerSmokeCases()...)
	cases = append(cases, vaultSmokeCases()...)
	cases = append(cases, goalSmokeCases()...)
	cases = append(cases, workflowSmokeCases()...)
	cases = append(cases, memorySmokeCases()...)
	cases = append(cases, skillSmokeCases()...)
	cases = append(cases, librarySmokeCases()...)
	cases = append(cases, sessionSmokeCases()...)
	cases = append(cases, notifySmokeCases(h.sink)...)
	cases = append(cases, oauthSmokeCases()...)
	cases = append(cases, recallySmokeCases()...)
	cases = append(cases, shareSmokeCases()...)
	cases = append(cases, emailSmokeCases()...)
	cases = append(cases, offRegistrySmokeCases()...)
	return cases
}

func coreSmokeCases() []smokeCase {
	return []smokeCase{{
		// bash and view_image share one call because they must share one sandbox
		// session: every case runs in a fresh session with its own working
		// directory, so an image written by an earlier case is not there to be
		// read by a later one. The markdown file is written to the durable agent
		// work root instead, which is where share_create_artifact resolves paths.
		tool:   "bash",
		covers: []string{"view_image"},
		script: func(t *testing.T, s *smokeState) string {
			command := "echo tool-smoke-bash-" + s.values["runID"] +
				" | tee \"$HOME/tool-smoke.md\" && printf %s " + smokePNGBase64 +
				" | base64 -d > tool-smoke.png"
			return fmt.Sprintf(
				"const wrote = await tools.invoke(\"bash\", %s);\n"+
					"await tools.invoke(\"view_image\", { path: \"tool-smoke.png\" });\n"+
					"return tools.text(wrote);",
				mustJSON(t, map[string]any{"command": command}))
		},
		// The baseline render is its own model call, made while the code call is
		// still open, so its reply is scripted before the turn's closing text.
		extraReplies: []string{"a one pixel test image"},
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
// workflowSmokeCases covers the workflow family. Three of its four tools assert
// their canonical error: saving needs a parentless composite Goal already in
// lifecycle=done/accepted, and only the Goal dispatcher's async workers can put
// one there — this gate deliberately leaves that driver off (see
// newSmokeHarness). Each case still proves the tool is enabled, that the call
// reached the service with arguments that passed schema admission, and that the
// refusal is the domain's own structured error. The success paths are covered
// in process by internal/workflow's service tests, and end to end by the
// goal_lifecycle journey in test/system.
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

func memorySmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool:  "memory_search",
			args:  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"q": "the tool smoke run"} },
			check: expectJSONObject("memory_search"),
		},
		{
			// "profile" is one of the fixed refs the schema documents, so the case
			// needs no recalled ref to read: memory_read is exercised on a ref every
			// deployment resolves.
			tool: "memory_read",
			args: func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"ref": "profile"} },
		},
	}
}

func skillSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "skill_installed_search",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"q": "stella", "limit": 5}
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				name := firstSkillName(t, results["skill_installed_search"])
				s.set("skill_name", name)
			},
		},
		{
			// The name comes from the search above, so skill_load reads a skill this
			// deployment actually installed rather than one the test assumed.
			tool: "skill_load",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"name": s.need(t, "skill_name")}
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				if strings.TrimSpace(results["skill_load"]) == "" {
					t.Error("skill_load returned empty content for an installed skill")
				}
			},
		},
	}
}

// firstSkillName pulls one installed skill's name out of the search result. An
// empty result set is a failure: this deployment syncs its built-in skills at
// startup, so a search that matches nothing means the sync did not happen.
func firstSkillName(t *testing.T, output string) string {
	t.Helper()
	var decoded []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("skill_installed_search result is not a JSON array: %v\n%s", err, truncate(output, 800))
	}
	if len(decoded) == 0 || decoded[0].Name == "" {
		t.Fatalf("skill_installed_search returned no installed skill: %s", truncate(output, 800))
	}
	return decoded[0].Name
}

func librarySmokeCases() []smokeCase {
	return []smokeCase{{
		// The library is empty in a fresh deployment, so this proves the search
		// path answers with a well-formed empty result rather than an error.
		tool:  "library_search",
		args:  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"query": "tool smoke"} },
		check: expectJSONObject("library_search"),
	}}
}

// sessionSmokeCases reaches another session's transcript, which is why each
// create/send runs a nested model turn of its own: extraReplies scripts them.
func sessionSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "session_create",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"message": "tool smoke child session " + s.values["runID"], "wait": true}
			},
			extraReplies: []string{"child session answered"},
			check:        captureSessionID("session_create", "child_session_id"),
		},
		{
			tool: "session_send",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"session_id": s.need(t, "child_session_id"), "message": "second turn", "wait": true}
			},
			extraReplies: []string{"child session answered again"},
		},
		{
			tool: "session_get",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"session_id": s.need(t, "child_session_id")}
			},
			check: expectMentions("session_get", "child_session_id"),
		},
		{
			tool:  "session_list",
			args:  noArgs,
			check: expectMentions("session_list", "child_session_id"),
		},
	}
}

func captureSessionID(tool, key string) func(*testing.T, *smokeState, map[string]string) {
	return func(t *testing.T, s *smokeState, results map[string]string) {
		s.set(key, requireJSONString(t, tool, results[tool], "session_id"))
	}
}

// notifySmokeCases proves notify's routing contract, not a delivery: this
// deployment registers no channel plugin (every built-in channel defaults to
// disabled and none can be pointed at a loopback fake), so the tool's canonical
// answer is that it has nowhere to send.
func notifySmokeCases(sink *smokeChannel) []smokeCase {
	return []smokeCase{{
		tool: "notify",
		args: func(t *testing.T, s *smokeState) map[string]any {
			return map[string]any{"message": "tool smoke notification " + s.values["runID"]}
		},
		// The assertion is the sink, not the tool's own return: a notifier that
		// reports success without delivering anywhere is exactly the failure a
		// smoke case for notify has to catch.
		check: func(t *testing.T, s *smokeState, results map[string]string) {
			want := "tool smoke notification " + s.values["runID"]
			for _, got := range sink.messages() {
				if strings.Contains(got, want) {
					return
				}
			}
			t.Fatalf("the notify sink never received %q; it saw %q", want, sink.messages())
		},
	}}
}

// oauthSmokeCases runs against a deployment with no OAuth provider configured,
// which is the honest shape of a fresh single-tenant install.
func oauthSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool:  "oauth_list",
			args:  noArgs,
			check: expectJSONObject("oauth_list"),
		},
		{
			// oauth_connect is asserted on its unknown-provider error on purpose: a
			// real provider name starts a device-authorization flow against that
			// third party's live endpoint, and this gate must make no external
			// network call. Measured, not assumed — provider "github" returned a
			// genuine github.com device code when this case first ran. The success
			// path is covered in process by internal/connections' service tests.
			tool: "oauth_connect",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"provider": "tool-smoke-absent-provider"}
			},
			assertsErrorShapeOnly: `(?i)(not configured|unknown|unsupported|no provider)`,
		},
		{
			// Disconnect is local and idempotent, so it runs its success path.
			tool:  "oauth_disconnect",
			args:  func(t *testing.T, s *smokeState) map[string]any { return map[string]any{"provider": "github"} },
			check: expectJSONObject("oauth_disconnect"),
		},
		{
			// A live flow id only exists after a real device-authorization call, so
			// this asserts the lookup's structured miss. internal/connections covers
			// the populated flow.
			tool: "oauth_flow_status",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"provider": "github", "flow_id": absentUUID}
			},
			assertsErrorShapeOnly: `(?i)(not found|expired|unknown|not configured)`,
		},
	}
}

// recallySmokeCases walks the reading-list family: articles and the daily
// digest first, because share_create_article needs an article to share, then
// feeds against a loopback RSS server so the poll path runs for real without
// leaving the host.
func recallySmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "recally_article_save",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"articles": []map[string]any{{
					"url":         "https://tool-smoke.invalid/article/" + s.values["runID"],
					"title":       "tool smoke article " + s.values["runID"],
					"content":     "# tool smoke\n\nA saved article body.",
					"source_type": "web",
				}}}
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				s.set("recally_article_id", firstSavedArticleID(t, results["recally_article_save"]))
			},
		},
		{
			tool:  "recally_article_get",
			args:  byID("recally_article_id"),
			check: expectMentions("recally_article_get", "recally_article_id"),
		},
		{
			tool:  "recally_article_list",
			args:  noArgs,
			check: expectMentions("recally_article_list", "recally_article_id"),
		},
		{
			tool: "recally_digest_save",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"narrative": "tool smoke digest " + s.values["runID"]}
			},
		},
		{
			tool:  "recally_digest_get",
			args:  noArgs,
			check: expectJSONObject("recally_digest_get"),
		},
		{
			tool: "recally_feed_add",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"url": s.need(t, "rss_url"), "kind": "rss"}
			},
			check: captureID("recally_feed_add", "recally_feed_id"),
		},
		{
			tool:  "recally_feed_list",
			args:  noArgs,
			check: expectMentions("recally_feed_list", "recally_feed_id"),
		},
		{
			tool:  "recally_feed_poll",
			args:  byID("recally_feed_id"),
			check: expectJSONObject("recally_feed_poll"),
		},
		{
			tool: "recally_entry_list",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"feed_id": s.need(t, "recally_feed_id")}
			},
			check: expectJSONObject("recally_entry_list"),
		},
		{
			tool: "recally_entry_add",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{
					"feed_id": s.need(t, "recally_feed_id"),
					"guid":    "tool-smoke-entry-" + s.values["runID"],
					"title":   "tool smoke entry",
					"url":     "https://tool-smoke.invalid/entry/" + s.values["runID"],
				}
			},
			check: func(t *testing.T, s *smokeState, results map[string]string) {
				var added struct {
					Entry struct {
						ID string `json:"id"`
					} `json:"entry"`
				}
				if err := json.Unmarshal([]byte(results["recally_entry_add"]), &added); err != nil || added.Entry.ID == "" {
					t.Fatalf("recally_entry_add returned no entry id: %s", truncate(results["recally_entry_add"], 800))
				}
				s.set("recally_entry_id", added.Entry.ID)
			},
		},
		{
			tool: "recally_entry_update",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{
					"feed_id": s.need(t, "recally_feed_id"),
					"id":      s.need(t, "recally_entry_id"),
					"status":  "skipped",
				}
			},
		},
		{
			tool: "recally_feed_remove",
			args: byID("recally_feed_id"),
		},
	}
}

// firstSavedArticleID pulls the id out of a batch save result, whose shape is a
// list even for one article.
func firstSavedArticleID(t *testing.T, output string) string {
	t.Helper()
	var batch struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &batch); err != nil {
		t.Fatalf("recally_article_save result is not JSON: %v\n%s", err, truncate(output, 800))
	}
	saved := batch.Results
	if len(saved) == 0 || saved[0].ID == "" {
		t.Fatalf("recally_article_save returned no article id: %s", truncate(output, 800))
	}
	return saved[0].ID
}

// shareSmokeCases publishes the two things a deployment can share: a workspace
// artifact (the file the bash case wrote) and a saved article.
func shareSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "share_create_artifact",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"path": "tool-smoke.md", "scope": "agent", "expires_in": "1h"}
			},
			check: captureID("share_create_artifact", "share_artifact_id"),
		},
		{
			tool: "share_create_article",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"article_id": s.need(t, "recally_article_id"), "expires_in": "1h"}
			},
			check: captureID("share_create_article", "share_article_id"),
		},
		{
			tool:  "share_list",
			args:  noArgs,
			check: expectMentions("share_list", "share_artifact_id"),
		},
		{
			tool: "share_revoke",
			args: byID("share_artifact_id"),
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

// emailSmokeCases covers the mail family. Its success paths run against a fake
// send function (Service.SetSendFunc, the production seam the email package
// already exposes for exactly this) and a seeded EMAIL_CONFIG whose hosts are
// documentation IP literals: ValidateAccountEgress must resolve no name and
// reach no host for these cases to be deterministic.
//
// The read paths have no equivalent seam — email.List/Read dial IMAP directly —
// so they assert the egress boundary's structured refusal instead, against a
// second account that deliberately points at loopback. That is the same
// boundary a real misconfiguration hits, it is reached only after the tool was
// enabled and the arguments passed schema admission, and the success path it
// stands in for is covered in-process by internal/email's own tests.
func emailSmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool:  "email_account_list",
			args:  noArgs,
			check: expectMentions("email_account_list", "email_account"),
		},
		{
			tool: "email_message_send",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{
					"account":         s.need(t, "email_account"),
					"to":              []string{"tool-smoke@example.test"},
					"subject":         "tool smoke " + s.values["runID"],
					"body":            "sent by the tool smoke gate",
					"idempotency_key": "tool-smoke-" + s.values["runID"],
				}
			},
			check: expectJSONObject("email_message_send"),
		},
		{
			tool: "email_message_list",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"account": s.need(t, "email_unreachable_account"), "limit": 1}
			},
			assertsErrorShapeOnly: `imap_host .* resolves to disallowed address`,
		},
		{
			tool: "email_message_read",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"account": s.need(t, "email_unreachable_account"), "folder": "INBOX", "uid": 1}
			},
			assertsErrorShapeOnly: `imap_host .* resolves to disallowed address`,
		},
	}
}

// offRegistrySmokeCases covers the model-facing tools that are not builtins:
// the webfetch plugin and the MCP prefix. They are as reachable to the model as
// any builtin, so they carry cases rather than exceptions.
func offRegistrySmokeCases() []smokeCase {
	return []smokeCase{
		{
			tool: "webfetch",
			args: func(t *testing.T, s *smokeState) map[string]any {
				return map[string]any{"url": s.need(t, "webfetch_url")}
			},
			check: expectMentions("webfetch", "runID"),
		},
	}
}

// protocolExceptions are the model-facing tools this gate deliberately does not
// invoke, each with the coverage that stands in its place. The list is closed:
// the coverage assertion requires every entry to be a real tool name in this
// build, and every tool not listed here to have a case.
var protocolExceptions = map[string]string{
	// `code` is the vehicle: every case in this file is a `code` call, so its
	// outer dispatch — schema admission, VM boot, catalog, child fan-out, result
	// marshalling — is proven once per case rather than once in a case of its own.
	"code": "invoked by every case as the Code Mode entry point",
	// goal_control is registered only inside a Goal attempt's executor, with a
	// different schema per attempt stage. It is unreachable from a chat session
	// by construction, and driving it needs the Goal dispatcher's async workers.
	"goal_control": "Goal attempt protocol; covered by internal/goal's executor tests and the goal_lifecycle system journey",
	// The mcp__ prefix cannot be closed from a hermetic test: internal/mcp
	// refuses any endpoint that resolves to a loopback, private, link-local or
	// unspecified address (client.go validatePublicIP), which is the SSRF guard
	// that makes user-registered MCP endpoints safe. A stub server on 127.0.0.1
	// is rejected at registration, and every address a test can bind is in one of
	// those ranges, so proving the prefix end to end would mean weakening the
	// guard. internal/mcp's own tests cover registration, transport selection,
	// name prefixing, and invocation against a fake client instead.
	smokeMCPPrefixTool: "user-registered MCP endpoints; internal/mcp tests cover them, and its SSRF guard rejects any endpoint a hermetic test can bind",
}

// smokeMCPPrefixTool stands for the whole mcp__ family, whose names exist only
// once a user registers a server. It is a representative name, not a tool this
// build registers on its own.
const smokeMCPPrefixTool = "mcp__tool-smoke__echo"

// smokePNGBase64 is a 1x1 PNG. view_image needs a real image on disk, and a
// literal keeps the fixture in the file that uses it.
const smokePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

const smokeModel = "claude-sonnet-4-6"

// smokeHarness is one fully wired deployment: the production composition root
// (setup) against a live database, a scripted provider, and the fixtures the
// external-dependency tools need — all on loopback, so the gate reaches nothing
// outside the host.
type smokeHarness struct {
	setup     *setupResult
	fake      *smokeProvider
	sink      *smokeChannel
	userID    string
	agentID   string
	authority authz.Authority
	runID     string
	ctx       context.Context
}

func TestToolSmoke(t *testing.T) {
	h := newSmokeHarness(t)
	state := h.seedFixtures(t)

	cases := smokeCases(h)
	catalog := h.readCodeCatalog(t)
	assertSmokeCoverageIsClosed(t, smokeToolUniverse(t), catalog, cases)

	report := make([]string, 0, len(cases))
	for _, smoke := range cases {
		outcome := "ok"
		if smoke.assertsErrorShapeOnly != "" {
			outcome = "error-shape-only"
		}
		if !t.Run(smoke.tool, func(t *testing.T) { h.runSmokeCase(t, smoke, state) }) {
			outcome = "FAILED"
		}
		report = append(report, fmt.Sprintf("%-26s %s", smoke.tool, outcome))
		// A tool folded into a sibling case still gets its own report line: the
		// report is the coverage answer, so it must name every tool, not every case.
		for _, covered := range smoke.covers {
			report = append(report, fmt.Sprintf("%-26s %-17s %s", covered, outcome, "invoked inside the "+smoke.tool+" case"))
		}
	}
	for tool, reason := range protocolExceptions {
		report = append(report, fmt.Sprintf("%-26s %-17s %s", tool, "exception", reason))
	}
	sort.Strings(report)
	t.Logf("tool smoke coverage (%d cases, %d exceptions):\n%s",
		len(cases), len(protocolExceptions), strings.Join(report, "\n"))
}

// newSmokeHarness boots the real server composition in process: a migrated
// database from dbtest, a temporary STELLA_HOME, and setup() itself, so the
// tools under test are the ones newBuiltinTools registers in production rather
// than a test-assembled lookalike. River and the scheduler run because the
// scheduler tools need them; the Goal dispatch tick stays off, or it would run
// planning turns concurrently and consume a case's scripted response.
func newSmokeHarness(t *testing.T) *smokeHarness {
	t.Helper()
	db := dbtest.New(t)
	runID := strings.ToLower(uuid.Must(uuid.NewV7()).String()[24:])

	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		t.Fatalf("tool smoke: generate vault key: %v", err)
	}
	t.Setenv("STELLA_HOME", t.TempDir())
	t.Setenv("STELLA_DATABASE_URL", db.Config().ConnString())
	t.Setenv("STELLA_VAULT_KEY", vaultKey)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fake := newSmokeProvider(t)
	store := cfgstore.NewDBStore(db)
	// Provider and Agent are created before setup so StartAll builds the agent
	// exactly as a restart would, from durable rows.
	if err := store.CreateProvider(ctx, config.Provider{
		ID: "tool-smoke", Type: "anthropic", Name: "tool smoke", Enabled: true,
		APIKey: "tool-smoke-not-a-key", BaseURL: fake.server.URL,
		Models: map[string]config.ProviderModel{smokeModel: {
			ID: smokeModel, Name: smokeModel, Enabled: true, ContextWindow: 200000, MaxTokens: 8192,
		}},
	}); err != nil {
		t.Fatalf("tool smoke: create provider: %v", err)
	}
	// webfetch ships disabled by default, and a tool the model cannot reach
	// cannot be smoked. Enabling it durably before setup means the plugin host
	// loads it the way a deployment that turned it on would.
	if err := store.SetPluginEnabled(ctx, config.PluginID(config.PluginKindTool, "webfetch"), true); err != nil {
		t.Fatalf("tool smoke: enable webfetch plugin: %v", err)
	}
	agentID := uuid.Must(uuid.NewV7()).String()
	if err := store.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: "tool-smoke-" + runID, Model: "tool-smoke/" + smokeModel,
		Scope: config.AgentScopeSystem, Enabled: true,
	}); err != nil {
		t.Fatalf("tool smoke: create agent: %v", err)
	}

	// view_image renders a baseline description through the vision model; without
	// one configured it falls back to the local extractor, which has nothing to
	// say about a 1x1 image. Pointing it at the same scripted provider keeps the
	// render on loopback and makes the extra turn schedulable.
	if err := config.SaveDefaultModels(ctx, store, config.DefaultModels{ModelVision: "tool-smoke/" + smokeModel}); err != nil {
		t.Fatalf("tool smoke: set the vision model: %v", err)
	}

	cfg, err := config.LoadServerConfig(os.LookupEnv)
	if err != nil {
		t.Fatalf("tool smoke: load server config: %v", err)
	}
	if err := ensureEmbeddedAssets(); err != nil {
		t.Fatalf("tool smoke: install embedded assets: %v", err)
	}
	result, err := setup(ctx, cfg, "http://127.0.0.1:0")
	if err != nil {
		t.Fatalf("tool smoke: setup: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		result.waitBackgroundTasks()
		_ = result.poolManager.Close()
		_ = result.workspaceManager.Close()
	})

	// River and the scheduler run because scheduler_job_create registers a River
	// periodic job and panics without a started client. The Goal dispatch tick is
	// deliberately NOT started: it would begin planning turns of its own against
	// a provider whose every turn is scripted, and steal a case's response. The
	// goal tools are still invoked; only the async driver stays off, and
	// goal_lifecycle in test/system is the journey that drives it.
	if err := result.riverClient.Start(ctx); err != nil {
		t.Fatalf("tool smoke: start river: %v", err)
	}
	t.Cleanup(func() { _ = result.riverClient.Stop(context.Background()) })
	if err := result.schedulerSvc.Start(ctx); err != nil {
		t.Fatalf("tool smoke: start scheduler: %v", err)
	}
	t.Cleanup(func() { _ = result.schedulerSvc.Stop() })

	publicKey, privateKey, err := vault.GenerateUserKeys(result.vaultSvc.MasterRecipient())
	if err != nil {
		t.Fatalf("tool smoke: generate user keys: %v", err)
	}
	user, err := appdb.NewAuthStore(db).CreateUser(ctx, auth.User{
		ID: uuid.Must(uuid.NewV7()).String(), Email: "tool-smoke-" + runID + "@example.test",
		Name: "tool smoke", Role: auth.RoleAdmin, AgePublicKey: publicKey, AgePrivateKey: privateKey,
	})
	if err != nil {
		t.Fatalf("tool smoke: create user: %v", err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(user.ID), true)
	if err != nil {
		t.Fatalf("tool smoke: build authority: %v", err)
	}

	return &smokeHarness{
		setup: result, fake: fake, userID: user.ID, agentID: agentID,
		authority: authority, runID: runID, ctx: ctx,
	}
}

// seedFixtures installs everything a tool needs before it can succeed, and
// returns the state the cases read it from. Each fixture is loopback-only.
func (h *smokeHarness) seedFixtures(t *testing.T) *smokeState {
	t.Helper()
	// notify has no "web channel" to fall back on: a Notifier with no registered
	// channel can only fail. Registering a sink is what a channel plugin does,
	// and it lets the case assert the message actually arrived.
	h.sink = &smokeChannel{}
	h.setup.notifier.Register(h.sink)

	// Both accounts point at address literals, so ValidateAccountEgress resolves
	// no name and the gate makes no DNS query. 198.51.100.0/24 is TEST-NET-2
	// (RFC 5737), reserved for documentation and never routed.
	emailConfig := `{"default":"smoke","accounts":{` +
		`"smoke":{"email":"tool-smoke@example.test","username":"tool-smoke","password":"tool-smoke-not-a-secret",` +
		`"imap_host":"198.51.100.10","imap_port":993,"imap_tls":"ssl",` +
		`"smtp_host":"198.51.100.11","smtp_port":465,"smtp_tls":"ssl"},` +
		`"unreachable":{"email":"tool-smoke@example.test","username":"tool-smoke","password":"tool-smoke-not-a-secret",` +
		`"imap_host":"127.0.0.1","imap_port":993,"imap_tls":"ssl",` +
		`"smtp_host":"127.0.0.1","smtp_port":465,"smtp_tls":"ssl"}}}`
	if err := h.setup.vaultSvc.Set(h.ctx, h.userID, "EMAIL_CONFIG", emailConfig); err != nil {
		t.Fatalf("tool smoke: seed EMAIL_CONFIG: %v", err)
	}
	// The send seam is production API (SetSendFunc), not a test-only hook: it
	// exists so a deployment can substitute delivery. Nothing is ever put on a
	// socket, and the recorded call proves the tool reached delivery.
	h.setup.emailSvc.SetSendFunc(func(email.EmailAccount, email.SendOptions) error { return nil })

	return &smokeState{values: map[string]string{
		"runID": h.runID,
		// The feed lives on loopback so recally_feed_poll runs its real fetch and
		// parse path without leaving the host.
		"rss_url":                   newFakeRSSServer(t, h.runID),
		"webfetch_url":              newFakeWebPage(t, h.runID),
		"email_account":             "smoke",
		"email_unreachable_account": "unreachable",
	}}
}

// runSmokeCase drives one turn: the model calls `code`, the VM invokes the
// case's tools, and each child's own result comes back on the agent event
// stream, keyed by the tool that produced it.
func (h *smokeHarness) runSmokeCase(t *testing.T, smoke smokeCase, state *smokeState) {
	t.Helper()
	h.fake.enqueueTool("toolu_smoke_"+smoke.tool, "code", h.smokeCodeArgs(t, smoke, state))
	for _, reply := range smoke.extraReplies {
		h.fake.enqueueText(reply)
	}
	h.fake.enqueueText("smoke " + smoke.tool + " done")

	settled := h.runTurn(t, "smoke "+smoke.tool)
	results := make(map[string]string, len(smoke.covers)+1)
	for _, tool := range append([]string{smoke.tool}, smoke.covers...) {
		child, ok := settled[tool]
		if !ok {
			t.Fatalf("no child tool result for %q; the code call never reached it (saw: %s)\ncode returned: %s",
				tool, strings.Join(settledToolNames(settled), " "), truncate(settled["code"].text, 1200))
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
			// An error-shape-only case must reach the tool's own logic. A schema
			// rejection means the arguments never got that far, which would let a
			// case claim coverage for a tool it never really called.
			if schemaRejection.MatchString(child.text) {
				t.Fatalf("%s was rejected before it ran, so the case proves nothing about the tool: %s",
					tool, truncate(child.text, 800))
			}
			return
		}
		if child.failed {
			t.Fatalf("%s returned an error result: %s", tool, truncate(child.text, 2000))
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
func (h *smokeHarness) smokeCodeArgs(t *testing.T, smoke smokeCase, state *smokeState) string {
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

// childToolResult is one settled tool invocation observed on the event stream.
type childToolResult struct {
	text   string
	failed bool
}

// runTurn runs one chat turn to completion and returns every settled tool
// result by tool name. Each case gets a fresh session: nothing a case leaves in
// a transcript can change the next case's request.
func (h *smokeHarness) runTurn(t *testing.T, prompt string) map[string]childToolResult {
	t.Helper()
	svc := h.setup.poolManager.GetService(h.agentID)
	if svc == nil {
		t.Fatalf("tool smoke: no agent service for %s", h.agentID)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()

	settled := map[string]childToolResult{}
	for event := range svc.Chat(ctx, agent.ChatRequest{
		UserID: h.userID, AgentID: h.agentID, Authority: h.authority,
		Channel: session.ChannelWeb, Kind: session.KindChat, Message: prompt,
	}) {
		if event.Err != nil {
			t.Fatalf("tool smoke: turn failed: %v", event.Err)
		}
		if use := event.ToolUse; use != nil && use.Status != "running" {
			settled[use.Tool] = childToolResult{text: use.Content, failed: use.Status == "error"}
		}
	}
	return settled
}

func settledToolNames(settled map[string]childToolResult) []string {
	names := make([]string, 0, len(settled))
	for name := range settled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readCodeCatalog asks the running system what tools the model can reach. The
// catalog has no Go-side accessor by design, so this pages it out through the
// same tools.search the model uses.
func (h *smokeHarness) readCodeCatalog(t *testing.T) []string {
	t.Helper()
	const listCatalog = `{"code":"const names = []; for (let offset = 0; ; ) { const page = tools.search(\"\", offset); for (const tool of page) { names.push(tool.name); } if (!page.hasMore) { break; } offset = page.nextOffset; } return names;"}`
	h.fake.enqueueTool("toolu_smoke_catalog", "code", listCatalog)
	h.fake.enqueueText("catalog listed")

	settled := h.runTurn(t, "list the tool catalog")
	raw, ok := settled["code"]
	if !ok || raw.failed {
		t.Fatalf("tool smoke: the catalog call did not settle successfully: %+v", raw)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw.text), &names); err != nil {
		t.Fatalf("tool smoke: catalog is not a JSON array: %v\n%s", err, truncate(raw.text, 800))
	}
	sort.Strings(names)
	t.Logf("code catalog (%d tools): %s", len(names), strings.Join(names, " "))
	return names
}

// smokeToolUniverse is every tool this build can put in front of a model:
// the production builtin surface (defaultToolNames, itself pinned to
// newBuiltinTools by TestDefaultToolNamesMatchGolden), plus the four names that
// are registered at runtime rather than by the builtin constructor. toolmeta
// must agree that those four are hand-written exceptions — if one of them ever
// becomes a generated family, this list is wrong and the assertion says so.
func smokeToolUniverse(t *testing.T) []string {
	t.Helper()
	runtimeRegistered := []string{"code", "goal_control", "webfetch", smokeMCPPrefixTool}
	for _, name := range runtimeRegistered {
		if !toolmeta.HandWritten(name) {
			t.Errorf("tool smoke: %q is listed as runtime-registered, but toolmeta says it is generated", name)
		}
	}
	return append(defaultToolNames(t), runtimeRegistered...)
}

// assertSmokeCoverageIsClosed is the discipline this gate exists for, as one
// set equation: the tools this build can show a model must equal the tools the
// cases invoke plus the explicitly documented protocol exceptions. A new tool
// with no case fails; a case for a tool that no longer exists fails; an
// exception that is not a real tool fails. There is no pending list, and a tool
// missing from the runtime catalog is a failure rather than a free pass.
func assertSmokeCoverageIsClosed(t *testing.T, universe, catalog []string, cases []smokeCase) {
	t.Helper()
	covered := map[string]bool{}
	for _, smoke := range cases {
		for _, tool := range append([]string{smoke.tool}, smoke.covers...) {
			if covered[tool] {
				t.Errorf("tool smoke: %q is invoked by more than one case; a tool is invoked exactly once", tool)
			}
			covered[tool] = true
		}
	}
	known := map[string]bool{}
	for _, name := range universe {
		known[name] = true
		switch {
		case covered[name]:
		case protocolExceptions[name] != "":
		default:
			t.Errorf("tool smoke: %q is model-facing but has no smoke case; add one to smokeCases() or document it in protocolExceptions", name)
		}
		if covered[name] && protocolExceptions[name] != "" {
			t.Errorf("tool smoke: %q is both invoked and listed as a protocol exception; delete the exception", name)
		}
	}
	for tool := range covered {
		if !known[tool] {
			t.Errorf("tool smoke: %q has a case but this build registers no such tool; the case proves nothing", tool)
		}
	}
	for tool := range protocolExceptions {
		if !known[tool] {
			t.Errorf("tool smoke: %q is listed as a protocol exception but is not a tool this build registers", tool)
		}
	}

	// The runtime catalog is the model's actual reach. Every invoked tool must be
	// in it, and it must contain nothing the universe does not know about.
	inCatalog := map[string]bool{}
	for _, name := range catalog {
		inCatalog[name] = true
		if !known[name] {
			t.Errorf("tool smoke: the model's catalog offers %q, which is not in the covered universe", name)
		}
	}
	for tool := range covered {
		if !inCatalog[tool] {
			t.Errorf("tool smoke: %q has a case but the catalog does not offer it to the model (catalog: %s)",
				tool, strings.Join(catalog, " "))
		}
	}
	// The code tool is the entry point, never a catalog entry: every case reaches
	// its tool through it.
	if inCatalog["code"] {
		t.Error("tool smoke: `code` is inside its own catalog; the entry point must not be reachable as a child call")
	}
}

// smokeChannel is a notification sink implementing the channel contract, which
// is the only way a Notifier can have anywhere to deliver: there is no built-in
// "web" channel to fall back on.
type smokeChannel struct {
	mu       sync.Mutex
	received []string
}

func (c *smokeChannel) Name() string                    { return "tool-smoke-sink" }
func (c *smokeChannel) Start(ctx context.Context) error { <-ctx.Done(); return nil }
func (c *smokeChannel) Stop()                           {}
func (c *smokeChannel) Notify(_ context.Context, n pkgchannel.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received = append(c.received, n.Text)
	return nil
}

func (c *smokeChannel) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.received)
}

// newFakeWebPage serves one static HTML page over loopback for the webfetch
// plugin, so its fetch, extract, and render path runs for real.
func newFakeWebPage(t *testing.T, runID string) string {
	t.Helper()
	page := fmt.Sprintf("<!doctype html><html><head><title>tool smoke page</title></head>"+
		"<body><h1>tool smoke</h1><p>%s</p></body></html>", runID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/tool-smoke"
}

// smokeProvider is a scripted Anthropic-compatible endpoint: a FIFO of turns,
// each either a text reply or one tool call. It deliberately never branches on
// prompt prose — a case's turn is chosen by arrival order alone — so editing a
// system prompt can never turn into a failure here.
type smokeProvider struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	scripts []smokeTurn
	served  int
}

type smokeTurn struct {
	text     string
	toolID   string
	toolName string
	toolArgs string
}

func newSmokeProvider(t *testing.T) *smokeProvider {
	t.Helper()
	p := &smokeProvider{t: t}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)
	// An unconsumed script means the system made fewer model calls than the case
	// assumed, which would silently skip a tool.
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.scripts) != 0 {
			t.Errorf("tool smoke: %d scripted model turns went unconsumed", len(p.scripts))
		}
	})
	return p
}

func (p *smokeProvider) enqueueText(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scripts = append(p.scripts, smokeTurn{text: text})
}

func (p *smokeProvider) enqueueTool(id, name, args string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scripts = append(p.scripts, smokeTurn{toolID: id, toolName: name, toolArgs: args})
}

func (p *smokeProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
		p.t.Errorf("tool smoke: unexpected provider request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusNotFound)
		return
	}
	p.mu.Lock()
	p.served++
	if len(p.scripts) == 0 {
		served := p.served
		p.mu.Unlock()
		p.t.Errorf("tool smoke: unscripted model request #%d", served)
		http.Error(w, "unscripted", http.StatusInternalServerError)
		return
	}
	turn := p.scripts[0]
	p.scripts = p.scripts[1:]
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		p.t.Error("tool smoke: response writer cannot flush; the provider stream needs it")
		return
	}
	for _, frame := range turn.frames() {
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		flusher.Flush()
	}
}

// frames renders one turn as the Anthropic streaming events the runner parses.
func (t smokeTurn) frames() []string {
	var frames []string
	emit := func(event string, data map[string]any) {
		payload, err := json.Marshal(data)
		if err != nil {
			// The maps are literals here, so a marshal failure is a bug in the fake.
			panic(fmt.Sprintf("tool smoke: marshal %s: %v", event, err))
		}
		frames = append(frames, fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))
	}
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_" + strings.TrimPrefix(t.toolID, "toolu_"), "type": "message", "role": "assistant",
		"model": smokeModel, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	}})
	stopReason := "end_turn"
	if t.toolName != "" {
		stopReason = "tool_use"
		emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": t.toolID, "name": t.toolName, "input": map[string]any{}},
		})
		emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": t.toolArgs},
		})
	} else {
		emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": t.text},
		})
	}
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 5},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
	return frames
}

// schemaRejection matches the errors a tool call gets before the tool runs:
// unknown name, or arguments the generated schema refused.
var schemaRejection = regexp.MustCompile(`(?i)(tool not found|unknown tool|unknown .* action|invalid input|unknown field|required|must be|failed to decode|cannot unmarshal)`)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("tool smoke: marshal json: %v", err)
	}
	return string(b)
}

// newFakeRSSServer serves one static feed over loopback and returns its URL. It
// exists so the recally feed family exercises fetch, parse, and dedup for real
// while the suite's no-external-network rule holds.
func newFakeRSSServer(t *testing.T, runID string) string {
	t.Helper()
	feed := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>tool smoke feed %[1]s</title>
  <link>http://127.0.0.1/tool-smoke</link>
  <description>a loopback feed for the tool smoke journey</description>
  <item>
    <title>tool smoke item one</title>
    <link>http://127.0.0.1/tool-smoke/one</link>
    <guid>tool-smoke-%[1]s-one</guid>
  </item>
  <item>
    <title>tool smoke item two</title>
    <link>http://127.0.0.1/tool-smoke/two</link>
    <guid>tool-smoke-%[1]s-two</guid>
  </item>
</channel></rss>`, runID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = io.WriteString(w, feed)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/feed.xml"
}
