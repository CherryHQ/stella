package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseExcludedToolsCanonicalizesTheList(t *testing.T) {
	got := parseExcludedTools(" write,read,write, ,edit ")
	want := []string{"edit", "read", "write"}
	if len(got) != len(want) {
		t.Fatalf("parseExcludedTools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseExcludedTools = %v, want %v", got, want)
		}
	}
	if got := parseExcludedTools(""); len(got) != 0 || got == nil {
		t.Fatalf("empty excluded tools = %#v, want non-nil empty list", got)
	}
}

func TestStreamTurnPassesExcludedToolsInTheRunRequest(t *testing.T) {
	var body struct {
		ExcludedTools []string `json:"excluded_tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\\n\\n"))
	}))
	defer server.Close()

	client := apiClient{baseURL: server.URL, token: "token", http: server.Client()}
	if _, _, err := client.streamTurn(t.Context(), "agent", "session", "work", []string{"edit", "read", "write"}); err != nil {
		t.Fatalf("streamTurn: %v", err)
	}
	want := []string{"edit", "read", "write"}
	for i := range want {
		if i >= len(body.ExcludedTools) || body.ExcludedTools[i] != want[i] {
			t.Fatalf("request excluded_tools = %v, want %v", body.ExcludedTools, want)
		}
	}
}

func TestStopAndConfirmStopsBeforeObservingTerminalState(t *testing.T) {
	stopped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/agents/a/sessions/s/stop":
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/agents/a/sessions/s":
			if !stopped {
				t.Fatal("terminal state was checked before stop")
			}
			_, _ = w.Write([]byte(`{"activity_status":"success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stopAndConfirm(ctx, apiClient{baseURL: server.URL, http: server.Client()}, "a", "s"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBindingRejectsMissingNonce(t *testing.T) {
	if _, err := writeBinding(t.TempDir(), "user", binding{Socket: "/tmp/bridge", Workdir: "/work"}); err == nil {
		t.Fatal("binding without nonce was accepted")
	}
}

// An MCP tool is not disableable, so its presence must void the run before any
// turn starts rather than produce a score with an unknown capability set.
func TestRunRefusesAnInstanceThatExposesMCPTools(t *testing.T) {
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/status":
			_, _ = w.Write([]byte(`{"sandbox_backend":"bridge"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/provisioned-users":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"provisioned_user":{"id":"rec"},"token":"tok"}`))
		case r.URL.Path == "/api/auth/me":
			_, _ = w.Write([]byte(`{"id":"acct"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/agents":
			_, _ = w.Write([]byte(`{"id":"agent"}`))
		case r.URL.Path == "/api/agents/agent/tools":
			_, _ = w.Write([]byte(`{"tools":[{"name":"bash","source":"core","enabled":true},{"name":"remote_search","source":"mcp","enabled":true}]}`))
		case r.Method == http.MethodPatch:
			patched = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	template := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(template, []byte(`{"socket":"/tmp/b.sock","nonce":"n","workdir":"/app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	instruction := filepath.Join(dir, "instruction.txt")
	if err := os.WriteFile(instruction, []byte("do the task"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "result.json")
	t.Setenv("STELLA_EVAL_ADMIN_TOKEN", "admin")
	os.Args = []string{
		"stella-eval-agent", "--stella-url", server.URL, "--instruction-file", instruction,
		"--binding-template", template, "--binding-dir", filepath.Join(dir, "bindings"), "--model", "p/m",
		"--user-id", "trial", "--deadline-seconds", "30", "--output", output,
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want %d", code, exitAdapter)
	}
	if patched {
		t.Fatal("driver tried to disable an MCP tool instead of voiding the run")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.MCPTools) != 1 || got.MCPTools[0] != "remote_search" || got.SessionID != "" {
		t.Fatalf("result must name the MCP tool and start no session: %+v", got)
	}
	if got.Model != "p/m" || got.CandidateCommit == "" {
		t.Fatalf("result must persist model and candidate commit: %+v", got)
	}
}

// The trajectory is what a failure taxonomy and a public run log are built
// from, so it must survive verbatim: fields this driver does not model are
// exactly the ones a later analysis will want.
func TestCollectEvidenceWritesTheTrajectoryVerbatim(t *testing.T) {
	body := `{"messages":[{"role":"user","token_count":4,"timestamp":"2026-08-19T10:00:00Z","reasoning":"kept"},` +
		`{"role":"assistant","token_count":9,"timestamp":"2026-08-19T10:00:01Z","provider_metadata":{"finish":"stop"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "trajectory.json")
	out := result{ToolCalls: map[string]int{}}

	client := apiClient{baseURL: server.URL, token: "t", http: server.Client()}
	if err := collectEvidence(context.Background(), client, "a", "s", path, &out); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != body {
		t.Errorf("trajectory was rewritten:\n got %s\nwant %s", written, body)
	}
	if out.TrajectoryPath != path || out.TrajectoryTruncated {
		t.Errorf("trajectory not recorded: %+v", out)
	}
	if out.Metrics.Turns != 1 {
		t.Errorf("metrics still derived from the same payload: %+v", out.Metrics)
	}
}

// A trajectory cut off at the page limit must say so; one that looks whole
// would mislabel a failure downstream.
func TestCollectEvidenceMarksATruncatedTrajectory(t *testing.T) {
	messages := make([]string, messageLimit)
	for i := range messages {
		messages[i] = `{"role":"assistant","timestamp":"2026-08-19T10:00:00Z"}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[` + strings.Join(messages, ",") + `]}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "trajectory.json")
	out := result{ToolCalls: map[string]int{}}

	client := apiClient{baseURL: server.URL, token: "t", http: server.Client()}
	if err := collectEvidence(context.Background(), client, "a", "s", path, &out); err != nil {
		t.Fatal(err)
	}

	if !out.TrajectoryTruncated {
		t.Error("a full page of history was not reported as truncated")
	}
}

func TestCollectUsageWaitsForAcceptedWrites(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents/a/sessions/s/usage" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		requests++
		if requests < 3 {
			_, _ = w.Write([]byte(`{"pending_call_count":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"pending_call_count":0,"call_count":2}`))
	}))
	defer server.Close()

	u, err := collectUsage(t.Context(), apiClient{baseURL: server.URL, http: server.Client()}, "a", "s")
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.CallCount != 2 || requests != 3 {
		t.Fatalf("usage = %+v after %d requests, want settled usage on third request", u, requests)
	}
}

// A deployment on any other backend runs the agent's commands outside the trial
// container. The bridge ledger proves that only afterwards, so the driver has
// to refuse before it provisions a user or starts a turn.
func TestRunRefusesAServerThatIsNotOnTheBridgeBackend(t *testing.T) {
	provisioned := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"sandbox_backend":"local"}`))
		case "/api/provisioned-users":
			provisioned = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"provisioned_user":{"id":"rec"},"token":"tok"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	template := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(template, []byte(`{"socket":"/tmp/b.sock","nonce":"n","workdir":"/app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	instruction := filepath.Join(dir, "instruction.txt")
	if err := os.WriteFile(instruction, []byte("do the task"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "result.json")
	t.Setenv("STELLA_EVAL_ADMIN_TOKEN", "admin")
	os.Args = []string{
		"stella-eval-agent", "--stella-url", server.URL, "--instruction-file", instruction,
		"--binding-template", template, "--binding-dir", filepath.Join(dir, "bindings"), "--model", "p/m",
		"--user-id", "trial", "--deadline-seconds", "30", "--output", output,
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want %d", code, exitAdapter)
	}
	if provisioned {
		t.Fatal("driver provisioned a user before checking where tools would run")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SandboxBackend != "local" || len(got.Errors) == 0 {
		t.Fatalf("result must name the backend it refused: %+v", got)
	}
}

// An older server that does not report the field is refused too: unknown is not
// bridge, and guessing here means guessing about where code executes.
func TestRunRefusesAServerThatDoesNotReportItsBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	template := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(template, []byte(`{"socket":"/tmp/b.sock","nonce":"n","workdir":"/app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	instruction := filepath.Join(dir, "instruction.txt")
	if err := os.WriteFile(instruction, []byte("do the task"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STELLA_EVAL_ADMIN_TOKEN", "admin")
	os.Args = []string{
		"stella-eval-agent", "--stella-url", server.URL, "--instruction-file", instruction,
		"--binding-template", template, "--binding-dir", filepath.Join(dir, "bindings"), "--model", "p/m",
		"--user-id", "trial", "--deadline-seconds", "30", "--output", filepath.Join(dir, "result.json"),
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want %d", code, exitAdapter)
	}
}

func TestFinishTimedOutExportsEvidenceEvenWhenStopIsNotConfirmed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
		default:
			_, _ = w.Write([]byte(`{"activity_status":"running"}`))
		}
	}))
	defer server.Close()
	trajectory := filepath.Join(t.TempDir(), "trajectory.json")
	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s"}
	code := finishTimedOut(apiClient{baseURL: server.URL, http: server.Client()}, &r, trajectory, func(*int64) {}, stopConfirmBudget)
	if code != exitAdapter {
		t.Fatalf("exit code = %d, want %d: an unconfirmed stop stays fail-closed", code, exitAdapter)
	}
	if !r.TimedOut {
		t.Fatal("the trial was not marked as timed out")
	}
	if r.TurnTerminalState != "" {
		t.Fatalf("terminal state = %q, want empty: stop was never confirmed", r.TurnTerminalState)
	}
	if _, err := os.Stat(trajectory); err != nil {
		t.Fatalf("trajectory was not exported: %v", err)
	}
}
