package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeriveDeadlinesRejectsExpiredAndInsufficientFinalizeBy(t *testing.T) {
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name       string
		finalizeBy time.Time
		want       string
	}{
		{name: "expired", finalizeBy: now, want: "already elapsed"},
		{name: "insufficient", finalizeBy: now.Add(time.Second), want: "leaves no working time"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := deriveDeadlines(tt.finalizeBy.UnixMilli(), time.Second, now)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("deriveDeadlines error = %v, want %q", err, tt.want)
			}
		})
	}

	work, finalize, err := deriveDeadlines(now.Add(3*time.Second).UnixMilli(), time.Second, now)
	if err != nil || !work.Equal(now.Add(2*time.Second)) || !finalize.Equal(now.Add(3*time.Second)) {
		t.Fatalf("deriveDeadlines = (%s, %s, %v), want +2s, +3s, nil", work, finalize, err)
	}
}

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

func TestStreamTurnHonorsDeadlineWhenTheServerNeverFinishes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, _, err := (apiClient{baseURL: server.URL, token: "token", http: server.Client()}).streamTurn(ctx, "a", "s", "work", nil)
	if err == nil {
		t.Fatal("streamTurn did not stop at the request deadline")
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
		"--user-id", "trial", "--finalize-by-unix-ms", fmt.Sprint(time.Now().Add(30 * time.Second).UnixMilli()),
		"--finalization-budget-seconds", "1", "--output", output,
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
		"--user-id", "trial", "--finalize-by-unix-ms", fmt.Sprint(time.Now().Add(30 * time.Second).UnixMilli()),
		"--finalization-budget-seconds", "1", "--output", output,
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
		"--user-id", "trial", "--finalize-by-unix-ms", fmt.Sprint(time.Now().Add(30 * time.Second).UnixMilli()),
		"--finalization-budget-seconds", "1", "--output", filepath.Join(dir, "result.json"),
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want %d", code, exitAdapter)
	}
}

func TestSpecializedTaskIdentityRejectsCrossTaskVerdicts(t *testing.T) {
	for _, task := range []string{"skill-bash-guard", "memory-library-evidence", "mcp-recally"} {
		got, err := parseSpecializedTask(task)
		if err != nil || string(got) != task {
			t.Fatalf("parseSpecializedTask(%q) = %q, %v", task, got, err)
		}
	}
	if _, err := parseSpecializedTask("other-task"); err == nil {
		t.Fatal("unknown task identity was accepted")
	}
}

func TestSpecializedFixturePlanSeedIsDeterministicAndSecretless(t *testing.T) {
	seed := "fixture-plan-secret-canary"
	first, firstDigest, err := newSpecializedFixture(taskSkillBashGuard, seed)
	if err != nil {
		t.Fatal(err)
	}
	second, secondDigest, err := newSpecializedFixture(taskSkillBashGuard, seed)
	if err != nil {
		t.Fatal(err)
	}
	_, otherDigest, err := newSpecializedFixture(taskSkillBashGuard, "other-fixture-plan-secret")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.artifact) != string(second.artifact) || firstDigest != secondDigest || firstDigest == otherDigest {
		t.Fatalf("fixture plan did not bind seed deterministically: %q %q %q", firstDigest, secondDigest, otherDigest)
	}
	encoded, err := json.Marshal(result{FixturePlanDigest: firstDigest})
	if err != nil || strings.Contains(string(encoded), seed) || strings.Contains(string(encoded), string(first.artifact)) {
		t.Fatalf("fixture result leaked seed or token: %s", encoded)
	}
	if _, _, err := newSpecializedFixture(taskSkillBashGuard, ""); err == nil {
		t.Fatal("specialized fixture accepted an empty plan seed")
	}
}

func bridgeArtifactBinding(t *testing.T, artifact []byte) binding {
	t.Helper()
	reserved, err := os.CreateTemp("/tmp", "stella-eval-bridge-")
	if err != nil {
		t.Fatal(err)
	}
	socket := reserved.Name()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request map[string]any
		if json.NewDecoder(conn).Decode(&request) != nil || request["op"] != "read_file" || request["verifier"] != true {
			return
		}
		_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "data": base64.StdEncoding.EncodeToString(artifact)})
	}()
	return binding{Socket: socket, Nonce: "bridge-nonce", Workdir: "/workspace"}
}

func TestMemoryLibraryEvidenceVerifierUsesOnlyTheContainerArtifact(t *testing.T) {
	fixture, _, err := newSpecializedFixture(taskMemoryLibraryEvidence, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := verifyMemoryLibraryEvidence(t.Context(), bridgeArtifactBinding(t, fixture.artifact), fixture)
	if err != nil || verdict.Reward != 1 || !verdict.Valid {
		t.Fatalf("success verdict = %+v, %v", verdict, err)
	}
	verdict, err = verifyMemoryLibraryEvidence(t.Context(), bridgeArtifactBinding(t, []byte("wrong\n")), fixture)
	if err != nil || verdict.Reward != 0 || !verdict.Valid {
		t.Fatalf("wrong artifact verdict = %+v, %v", verdict, err)
	}
}

func TestSkillBashGuardVerifierSeparatesWrongArtifactFromBridgeFailure(t *testing.T) {
	fixture, _, err := newSpecializedFixture(taskSkillBashGuard, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := verifySkillBashGuard(t.Context(), bridgeArtifactBinding(t, fixture.artifact), fixture)
	if err != nil || verdict.Reward != 1 || !verdict.Valid {
		t.Fatalf("success verdict = %+v, %v", verdict, err)
	}
	verdict, err = verifySkillBashGuard(t.Context(), bridgeArtifactBinding(t, []byte("wrong\n")), fixture)
	if err != nil || verdict.Reward != 0 || !verdict.Valid {
		t.Fatalf("wrong artifact verdict = %+v, %v", verdict, err)
	}
	verdict, err = verifySkillBashGuard(t.Context(), bridgeArtifactBinding(t, nil), fixture)
	if err != nil || verdict.Reward != 0 || !verdict.Valid {
		t.Fatalf("empty artifact verdict = %+v, %v", verdict, err)
	}
	// A fixed artifact that exceeds the bridge cap is still wrong task output,
	// never an adapter escape hatch that removes the trial from the denominator.
	if !artifactBusinessFailure(errors.New("bridge artifact read rejected: too_large")) || !artifactBusinessFailure(errors.New("bridge artifact read rejected: non_regular")) {
		t.Fatal("wrong fixed artifact shape was not classified as business failure")
	}
	if _, err = verifySkillBashGuard(t.Context(), binding{Socket: "/tmp/no-such-stella-bridge.sock", Nonce: "n"}, fixture); err == nil {
		t.Fatal("bridge failure was scored as business failure")
	}
}

func TestMCPRecallyVerifierChecksPlanDuplicateAndAPIAvailability(t *testing.T) {
	turnStarted := time.Now().UTC()
	plan := fixtureConfig{
		ArticleCanonicalURL:  "https://fixture.invalid/article/amber-meadow",
		ArticleTitle:         "Amber Meadow",
		ArticleContentDigest: sha256Digest([]byte("amber meadow")),
	}
	inspection := fixtureInspection{Version: 1, Complete: true, CatalogCount: specializedCatalogCount, ChainComplete: true, AckWriteCount: 1}
	mode := "success"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/recally/articles") && r.URL.Query().Get("canonical_url") != "":
			if mode == "api-invalid" {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			count := 1
			if mode == "duplicate" {
				count = 2
			}
			articles := make([]map[string]any, count)
			for i := range articles {
				articles[i] = map[string]any{"id": "article", "canonical_url": plan.ArticleCanonicalURL, "title": plan.ArticleTitle, "created_at": turnStarted.Add(time.Second)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"articles": articles})
		case r.URL.Path == "/api/recally/articles/article":
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "amber meadow"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := apiClient{baseURL: server.URL, token: "token", http: server.Client()}

	verdict, err := verifyMCPRecally(t.Context(), client, binding{Nonce: "nonce"}, turnStarted, plan, inspection)
	if err != nil || verdict.Reward != 1 || !verdict.Valid {
		t.Fatalf("success verdict = %+v, %v", verdict, err)
	}
	mode = "duplicate"
	verdict, err = verifyMCPRecally(t.Context(), client, binding{Nonce: "nonce"}, turnStarted, plan, inspection)
	if err != nil || verdict.Reward != 0 || !verdict.Valid {
		t.Fatalf("duplicate verdict = %+v, %v", verdict, err)
	}
	mode = "api-invalid"
	if _, err = verifyMCPRecally(t.Context(), client, binding{Nonce: "nonce"}, turnStarted, plan, inspection); err == nil {
		t.Fatal("Recally API failure was scored as business failure")
	}
	mode = "success"
	if verdict, err = verifyMCPRecally(t.Context(), client, binding{Nonce: "nonce"}, turnStarted, plan, fixtureInspection{Version: 1, Complete: true, CatalogCount: specializedCatalogCount, ChainComplete: true, AckWriteCount: 2, DuplicateWriteCount: 1}); err != nil || verdict.Reward != 0 {
		t.Fatalf("duplicate MCP write verdict = %+v, %v", verdict, err)
	}
}

func TestFixtureCatalogAttestationRejectsMismatch(t *testing.T) {
	plan := fixtureConfig{CatalogDigest: "sha256:expected"}
	if !fixtureCatalogMatches(specializedCatalogCount, plan.CatalogDigest, plan) {
		t.Fatal("matching catalog attestation was rejected")
	}
	if fixtureCatalogMatches(specializedCatalogCount-1, plan.CatalogDigest, plan) || fixtureCatalogMatches(specializedCatalogCount, "sha256:other", plan) {
		t.Fatal("catalog attestation mismatch was accepted")
	}
}

func TestCleanupRetainsTheUserPATUntilUserScopedRetryCompletes(t *testing.T) {
	registrationFails := true
	patActive := true
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		switch {
		case req.URL.Path == "/api/auth/me":
			if req.Header.Get("Authorization") != "Bearer user-pat" || !patActive {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"account"}`))
		case req.Method == http.MethodDelete && req.URL.Path == "/api/mcp/servers/registration":
			if req.Header.Get("Authorization") != "Bearer user-pat" {
				t.Fatal("registration cleanup did not use the provisioned-user PAT")
			}
			if registrationFails {
				http.Error(w, "transient", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/api/agents/agent":
			if req.Header.Get("Authorization") != "Bearer user-pat" || !patActive {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users/provisioned/deactivate":
			if req.Header.Get("Authorization") != "Bearer admin-pat" {
				t.Fatal("deactivation did not use the admin control")
			}
			patActive = false
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected cleanup request %s %s", req.Method, req.URL.RequestURI())
		}
	}))
	defer server.Close()
	user := apiClient{baseURL: server.URL, token: "user-pat", http: server.Client()}
	admin := apiClient{baseURL: server.URL, token: "admin-pat", http: server.Client()}

	first := result{AgentID: "agent", MCPRegistrationID: "registration"}
	if err := cleanupTrialResources(t.Context(), &first, user, admin, "provisioned"); err == nil {
		t.Fatal("transient cleanup failure was not reported")
	}
	if !patActive {
		t.Fatal("transient registration deletion revoked the PAT before retry")
	}
	if err := user.call(t.Context(), http.MethodGet, "/api/auth/me", nil, nil); err != nil {
		t.Fatalf("PAT was not usable after transient cleanup error: %v", err)
	}
	wantFirst := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "error"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "pending"},
	}
	if !reflect.DeepEqual(first.Cleanup, wantFirst) {
		t.Fatalf("first cleanup = %+v, want %+v", first.Cleanup, wantFirst)
	}
	for _, call := range calls {
		if strings.Contains(call, "/deactivate") {
			t.Fatalf("deactivated before retry completed: %v", calls)
		}
	}

	registrationFails = false
	second := result{AgentID: "agent", MCPRegistrationID: "registration"}
	if err := cleanupTrialResources(t.Context(), &second, user, admin, "provisioned"); err != nil {
		t.Fatal(err)
	}
	if patActive {
		t.Fatal("successful cleanup did not deactivate the provisioned user")
	}
	if err := user.call(t.Context(), http.MethodGet, "/api/auth/me", nil, nil); err == nil {
		t.Fatal("deactivation did not revoke the provisioned-user PAT")
	}
	wantSecond := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "completed"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "completed"},
	}
	if !reflect.DeepEqual(second.Cleanup, wantSecond) {
		t.Fatalf("second cleanup = %+v, want %+v", second.Cleanup, wantSecond)
	}
}

func TestEarlySpecializedAdmissionExitCompletesNormalCleanup(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	user := apiClient{baseURL: server.URL, token: "user-pat", http: server.Client()}
	admin := apiClient{baseURL: server.URL, token: "admin-pat", http: server.Client()}
	r := result{AgentID: "agent", MCPRegistrationID: "registration"}

	// This is the pre-turn path: admission can reject the freshly created agent
	// before a session exists, but its user-scoped registration must still leave
	// no recovery lease behind.
	if err := cleanupTrialResources(t.Context(), &r, user, admin, "provisioned"); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"DELETE /api/mcp/servers/registration?scope=user_agent&agent_id=agent",
		"DELETE /api/agents/agent",
		"POST /api/provisioned-users/provisioned/deactivate",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("cleanup calls = %v, want %v", calls, wantCalls)
	}
	want := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "completed"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "completed"},
	}
	if !reflect.DeepEqual(r.Cleanup, want) || len(r.Errors) != 0 {
		t.Fatalf("early cleanup = phases=%+v errors=%v", r.Cleanup, r.Errors)
	}
}

func TestRunCleansLibraryFixtureWhenVerificationFailsBeforeLeaseClaim(t *testing.T) {
	// The in-memory service model holds two distractors the driver must never
	// receive from its scoped list: another Agent's user_agent file and a
	// user-scoped file. Moving libraryFixture below verification makes Agent
	// deletion fail while the owned fixture remains, so this is a driver-level
	// regression test rather than a hand-assembled cleanup state.
	files := map[string]bool{"owned": true, "other-agent": true, "user-scope": true}
	mcpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/status":
			_, _ = w.Write([]byte(`{"sandbox_backend":"bridge"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"provisioned_user":{"id":"provisioned"},"token":"trial-token"}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/auth/me":
			_, _ = w.Write([]byte(`{"id":"account"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/agents":
			_, _ = w.Write([]byte(`{"id":"agent"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/users/me/memories/agent/knowledge":
			w.WriteHeader(http.StatusCreated)
		case req.Method == http.MethodPost && req.URL.Path == "/api/library-files":
			if req.URL.Query().Get("scope") != "user_agent" || req.URL.Query().Get("agent_id") != "agent" {
				t.Fatalf("upload scope = %q", req.URL.RawQuery)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"owned"}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/users/me/memories/agent/knowledge":
			// The upload completed, but verification fails before MCP registration
			// or lease claim. The deferred driver cleanup must still own it.
			_, _ = w.Write([]byte(`{"knowledge":[]}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/library-files":
			if req.URL.Query().Get("scope") != "user_agent" || req.URL.Query().Get("agent_id") != "agent" {
				t.Fatalf("cleanup list scope = %q", req.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"library_files":[{"id":"owned"}]}`))
		case req.Method == http.MethodDelete && req.URL.Path == "/api/library-files/owned":
			if !files["owned"] {
				t.Fatal("owned Library file was deleted twice")
			}
			files["owned"] = false
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/api/library-files/other-agent":
			t.Fatal("cleanup deleted another Agent's Library file")
		case req.Method == http.MethodDelete && req.URL.Path == "/api/library-files/user-scope":
			t.Fatal("cleanup deleted a user-scoped Library file")
		case req.Method == http.MethodDelete && req.URL.Path == "/api/agents/agent":
			if files["owned"] {
				http.Error(w, "owned Library file remains", http.StatusInternalServerError)
				return
			}
			if !files["other-agent"] || !files["user-scope"] {
				t.Fatal("cleanup mutated a Library distractor before Agent deletion")
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users/provisioned/deactivate":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(req.URL.Path, "/api/mcp/servers"):
			mcpCalls++
			t.Fatal("MCP registration or cleanup lease was reached after verification failure")
		default:
			t.Fatalf("unexpected driver request %s %s", req.Method, req.URL.RequestURI())
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	bindingTemplate := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(bindingTemplate, []byte(`{"socket":"/tmp/bridge.sock","nonce":"nonce","workdir":"/work"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	instruction := filepath.Join(dir, "instruction.txt")
	if err := os.WriteFile(instruction, []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(dir, "fixture.json")
	fixturePayload, err := json.Marshal(fixtureConfig{
		Version: 1, Authority: "http://127.0.0.1:1", RouteKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), CleanupSocket: "/tmp/fixture.sock",
		CatalogDigest: "sha256:catalog", ArticleCanonicalURL: "https://fixture.invalid/article", ArticleTitle: "fixture", ArticleContentDigest: "sha256:content", FixturePlanDigest: "sha256:plan", FixturePlanSeed: "seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, fixturePayload, 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupState := filepath.Join(dir, "cleanup-state.json")
	output := filepath.Join(dir, "result.json")

	oldArgs, oldCommandLine := os.Args, flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	})
	t.Setenv("STELLA_EVAL_ADMIN_TOKEN", "admin")
	os.Args = []string{
		"stella-eval-agent", "--stella-url", server.URL, "--instruction-file", instruction,
		"--binding-template", bindingTemplate, "--binding-dir", filepath.Join(dir, "bindings"), "--model", "provider/model",
		"--user-id", "trial", "--task-id", string(taskMemoryLibraryEvidence), "--mcp-fixture-config", fixturePath,
		"--cleanup-state", cleanupState, "--finalize-by-unix-ms", fmt.Sprint(time.Now().Add(30 * time.Second).UnixMilli()),
		"--finalization-budget-seconds", "1", "--output", output,
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want %d", code, exitAdapter)
	}
	if mcpCalls != 0 {
		t.Fatalf("MCP calls before lease claim = %d, want 0", mcpCalls)
	}
	if _, err := os.Stat(cleanupState); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup lease state exists before claim: %v", err)
	}
	if files["owned"] || !files["other-agent"] || !files["user-scope"] {
		t.Fatalf("Library cleanup state = %+v", files)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	wantCleanup := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "skipped"},
		{Phase: "library_files", Outcome: "completed"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "completed"},
	}
	if !reflect.DeepEqual(got.Cleanup, wantCleanup) || len(got.Errors) == 0 {
		t.Fatalf("driver cleanup = phases=%+v errors=%v", got.Cleanup, got.Errors)
	}
}

func TestLibraryFixtureCleanupRunsBeforeAgentAfterSeedVerificationFailure(t *testing.T) {
	var calls []string
	agentAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/library-files":
			_, _ = w.Write([]byte(`{"library_files":[{"id":"fixture-file"}]}`))
		case req.Method == http.MethodDelete && req.URL.Path == "/api/library-files/fixture-file":
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/api/agents/agent":
			agentAttempts++
			if agentAttempts == 1 {
				http.Error(w, "library cleanup pending", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users/provisioned/deactivate":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected cleanup request %s %s", req.Method, req.URL.RequestURI())
		}
	}))
	defer server.Close()

	r := result{AgentID: "agent", libraryFixture: true}
	if err := cleanupTrialResources(t.Context(), &r,
		apiClient{baseURL: server.URL, token: "user-pat", http: server.Client()},
		apiClient{baseURL: server.URL, token: "admin-pat", http: server.Client()}, "provisioned"); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"GET /api/library-files?scope=user_agent&agent_id=agent",
		"DELETE /api/library-files/fixture-file",
		"DELETE /api/agents/agent",
		"DELETE /api/agents/agent",
		"POST /api/provisioned-users/provisioned/deactivate",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("cleanup calls = %v, want %v", calls, wantCalls)
	}
	wantPhases := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "skipped"},
		{Phase: "library_files", Outcome: "completed"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "completed"},
	}
	if !reflect.DeepEqual(r.Cleanup, wantPhases) || len(r.Errors) != 0 {
		t.Fatalf("cleanup = phases=%+v errors=%v", r.Cleanup, r.Errors)
	}
}

func TestCleanupStateDoesNotSerializeTokenCanary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup-state.json")
	if err := writeCleanupState(path, cleanupState{Lease: "opaque-lease", ProvisionedUserID: "provisioned-user"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-canary") || strings.Contains(string(data), "token") {
		t.Fatalf("cleanup state leaked token-shaped data: %s", data)
	}
}

func TestFinishTimedOutPreservesRuntimeSurfaceFailureAfterBestEffortEvidence(t *testing.T) {
	evidenceRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			evidenceRequested = true
			http.Error(w, "evidence unavailable", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"activity_status":"success"}`))
		}
	}))
	defer server.Close()

	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s"}
	code := finishTimedOut(t.Context(), time.Now().Add(stopConfirmBudget), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, taskSkillBashGuard, &fixtureConfig{}, "", nil)
	if code != exitAdapter || r.FailureClass != "adapter" {
		t.Fatalf("timeout result = code %d class %q, want adapter invalid", code, r.FailureClass)
	}
	if !evidenceRequested {
		t.Fatal("runtime surface failure skipped best-effort evidence collection")
	}
	if len(r.Errors) != 2 || !strings.Contains(r.Errors[0], "runtime tool surface is missing") || !strings.Contains(r.Errors[1], "collect evidence after timeout") {
		t.Fatalf("errors must preserve runtime-surface first cause before evidence failure: %#v", r.Errors)
	}
}

func TestCollectRuntimeSurfaceDistinguishesMissingAndZeroTools(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing", body: `{}`, want: "runtime tool surface is missing"},
		{name: "zero", body: `{"tool_surface":{"strategy":"native","tools":[]}}`, want: "runtime tool surface has zero tools"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			err := collectRuntimeSurface(t.Context(), apiClient{baseURL: server.URL, http: server.Client()}, "a", "s", &result{})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("collectRuntimeSurface error = %v, want %q", err, tt.want)
			}
		})
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
	code := finishTimedOut(t.Context(), time.Now().Add(stopConfirmBudget), apiClient{baseURL: server.URL, http: server.Client()}, &r, trajectory, func(*int64) {}, "", nil, "", nil)
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

func TestFinishTimedOutHonorsParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s"}
	started := time.Now()
	cleanupRan := false
	code := finishTimedOut(parent, time.Now().Add(time.Second), apiClient{baseURL: "http://example.invalid", http: http.DefaultClient}, &r, "", func(*int64) {}, "", nil, "", func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
			t.Fatal("cleanup did not retain the original finalization deadline")
		}
		cleanupRan = true
		return nil
	})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled timeout finalization ran for %s", elapsed)
	}
	if code != exitAdapter || r.FailureClass != "adapter" || r.HostVerdict != nil || !cleanupRan {
		t.Fatalf("timeout result = code %d class %q cleanup=%t verdict=%+v, want typed adapter invalid", code, r.FailureClass, cleanupRan, r.HostVerdict)
	}
}

func TestFinishTimedOutReservesTheFinalizationWallForCleanup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s"):
			<-req.Context().Done()
		}
	}))
	defer server.Close()

	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s"}
	cleanupRan := false
	code := finishTimedOut(t.Context(), time.Now().Add(80*time.Millisecond), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, "", nil, "", func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Fatal("cleanup received an exhausted finalization context")
		}
		cleanupRan = true
		return nil
	})
	if code != exitAdapter || !cleanupRan {
		t.Fatalf("timeout result = code %d cleanup=%t, want invalid result after bounded cleanup", code, cleanupRan)
	}
}

func TestFinishTimedOutHonorsOneFinalizationWall(t *testing.T) {
	const budget = 50 * time.Millisecond
	for _, tt := range []struct {
		name string
		hang string
	}{
		{name: "session GET", hang: "session"},
		{name: "messages GET", hang: "messages"},
		{name: "usage poll", hang: "usage"},
		{name: "cleanup", hang: "cleanup"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if tt.hang == "session" && req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s") {
					<-req.Context().Done()
					return
				}
				if tt.hang == "messages" && req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages") {
					<-req.Context().Done()
					return
				}
				if tt.hang == "usage" && req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/usage") {
					<-req.Context().Done()
					return
				}
				if tt.hang == "cleanup" && req.Method == http.MethodDelete && req.URL.Path == "/api/agents/a" {
					<-req.Context().Done()
					return
				}
				switch {
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop"):
					w.WriteHeader(http.StatusNoContent)
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s"):
					_, _ = w.Write([]byte(`{"activity_status":"stopped"}`))
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages"):
					_, _ = w.Write([]byte(`{"messages":[]}`))
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/usage"):
					_, _ = w.Write([]byte(`{"pending_call_count":0}`))
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s"}
			var cleanup timeoutCleanup
			if tt.hang == "cleanup" {
				user := apiClient{baseURL: server.URL, http: server.Client()}
				cleanup = func(ctx context.Context) error {
					return cleanupTrialResources(ctx, &r, user, user, "provisioned")
				}
			}
			started := time.Now()
			code := finishTimedOut(t.Context(), time.Now().Add(budget), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, "", nil, "", cleanup)
			if elapsed := time.Since(started); elapsed > budget+300*time.Millisecond {
				t.Fatalf("finishTimedOut ran for %s, finalization budget is %s", elapsed, budget)
			}
			if code != exitAdapter || r.FailureClass != "adapter" || r.HostVerdict != nil {
				t.Fatalf("timeout result = code %d class %q verdict=%+v, want typed adapter invalid", code, r.FailureClass, r.HostVerdict)
			}
			if len(r.Errors) == 0 {
				t.Fatal("bounded finalization did not record its first failure")
			}
		})
	}
}

func startTimeoutFixtureSocket(t *testing.T, hang bool) (fixtureConfig, func()) {
	t.Helper()
	socket, err := os.CreateTemp("", "stella-fixture-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket.Name()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket.Name())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request map[string]string
		if json.NewDecoder(conn).Decode(&request) != nil || request["action"] != "inspect" {
			return
		}
		if hang {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		_, _ = conn.Write([]byte(`{"inspect":{"version":1,"complete":true,"catalog_count":53,"initialize_count":1,"initialized_notification_count":1,"tools_list_count":1}}\n`))
	}()
	return fixtureConfig{CleanupSocket: listener.Addr().String()}, func() {
		_ = listener.Close()
		_ = os.Remove(socket.Name())
	}
}

func TestInspectCleanupLeaseUsesTheParentDeadline(t *testing.T) {
	fixture, closeSocket := startTimeoutFixtureSocket(t, true)
	defer closeSocket()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := inspectCleanupLease(ctx, fixture.CleanupSocket, "lease"); err == nil {
		t.Fatal("hung fixture inspection succeeded")
	}
	if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
		t.Fatalf("fixture inspection exceeded its context deadline: %s", elapsed)
	}
}

func TestReleaseCleanupLeaseUsesTheBoundedSocketPath(t *testing.T) {
	socket, err := os.CreateTemp("", "stella-release-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket.Name()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socket.Name())
	}()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request map[string]string
		if json.NewDecoder(conn).Decode(&request) != nil || request["action"] != "release" || request["lease"] != "lease" {
			return
		}
		_, _ = conn.Write([]byte(`{"outcomes":["released"]}\n`))
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := releaseCleanupLease(ctx, socket.Name(), "lease"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupSpecializedTrialResourcesPersistsUserPhasesBeforeDeactivation(t *testing.T) {
	socket, err := os.CreateTemp("", "stella-specialized-cleanup-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket.Name()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socket.Name())
	}()
	actions := make(chan string, 2)
	go func() {
		for range 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request map[string]string
			if json.NewDecoder(conn).Decode(&request) == nil {
				actions <- request["action"]
				if request["action"] == "cleanup" {
					_, _ = conn.Write([]byte(`{"outcomes":["registration","library_files","agent"]}\n`))
				} else {
					_, _ = conn.Write([]byte(`{"outcomes":["released"]}\n`))
				}
			}
			_ = conn.Close()
		}
	}()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/provisioned-users/provisioned/deactivate" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer admin.Close()

	r := result{libraryFixture: true}
	if err := cleanupSpecializedTrialResources(t.Context(), &r, apiClient{baseURL: admin.URL, http: admin.Client()}, "provisioned", fixtureConfig{CleanupSocket: socket.Name()}, "lease"); err != nil {
		t.Fatal(err)
	}
	if got := []string{<-actions, <-actions}; !reflect.DeepEqual(got, []string{"cleanup", "release"}) {
		t.Fatalf("fixture cleanup actions = %v, want cleanup then release", got)
	}
	if !r.FixtureLeaseReleased {
		t.Fatal("specialized cleanup did not persist its released lease result")
	}
	want := []cleanupPhase{
		{Phase: "mcp_registration", Outcome: "completed"},
		{Phase: "library_files", Outcome: "completed"},
		{Phase: "agent", Outcome: "completed"},
		{Phase: "provisioned_user", Outcome: "completed"},
		{Phase: "fixture_lease", Outcome: "completed"},
	}
	if !reflect.DeepEqual(r.Cleanup, want) {
		t.Fatalf("cleanup phases = %+v, want %+v", r.Cleanup, want)
	}
}

func specializedTimeoutSurface(t *testing.T, hang bool) ([]byte, fixtureConfig, func()) {
	t.Helper()
	fixture, closeSocket := startTimeoutFixtureSocket(t, hang)
	tools := make([]map[string]any, 0, 5+specializedCatalogCount)
	for name := range laneCatalogTools() {
		tools = append(tools, map[string]any{"name": name})
	}
	mcpNames := make([]string, 0, specializedCatalogCount)
	for i := range specializedCatalogCount {
		name := fmt.Sprintf("mcp__fixture__%02d", i)
		mcpNames = append(mcpNames, name)
		tools = append(tools, map[string]any{"name": name})
	}
	fixture.CatalogDigest = digestNames(mcpNames)
	payload, err := json.Marshal(map[string]any{
		"activity_status": "stopped",
		"tool_surface":    map[string]any{"strategy": "native", "tools": tools},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload, fixture, closeSocket
}

func TestFinishTimedOutWritesZeroVerdictOnlyAfterCompleteEvidence(t *testing.T) {
	surface, fixture, closeSocket := specializedTimeoutSurface(t, false)
	defer closeSocket()
	var r result
	messagesObserved := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s"):
			_, _ = w.Write(surface)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages"):
			if r.HostVerdict != nil {
				t.Fatal("timeout verdict was attached before message evidence")
			}
			messagesObserved = true
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/usage"):
			if !messagesObserved || r.HostVerdict != nil {
				t.Fatal("timeout verdict was attached before complete evidence")
			}
			_, _ = w.Write([]byte(`{"pending_call_count":0}`))
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()

	r = result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s", BridgeNonce: "nonce", MCPRegistrationID: "registration", MCPTools: []string{specializedFixtureRegistrationName}}
	code := finishTimedOut(t.Context(), time.Now().Add(time.Second), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, taskSkillBashGuard, &fixture, "lease", nil)
	if code != exitTimeout || r.HostVerdict == nil || !r.HostVerdict.Valid || r.HostVerdict.Reward != 0 {
		t.Fatalf("timeout result = code %d verdict=%+v, want scoreable zero after evidence", code, r.HostVerdict)
	}
}

func TestFinishTimedOutBoundsHungFixtureInspection(t *testing.T) {
	surface, fixture, closeSocket := specializedTimeoutSurface(t, true)
	defer closeSocket()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s"):
			_, _ = w.Write(surface)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/usage"):
			_, _ = w.Write([]byte(`{"pending_call_count":0}`))
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()

	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s", MCPRegistrationID: "registration", MCPTools: []string{specializedFixtureRegistrationName}}
	started := time.Now()
	code := finishTimedOut(t.Context(), time.Now().Add(50*time.Millisecond), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, taskSkillBashGuard, &fixture, "lease", nil)
	if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
		t.Fatalf("fixture inspection exceeded finalization wall: %s", elapsed)
	}
	if code != exitAdapter || r.HostVerdict != nil || r.FailureClass != "adapter" {
		t.Fatalf("timeout result = code %d class %q verdict=%+v, want invalid without verdict", code, r.FailureClass, r.HostVerdict)
	}
}

func TestFinishTimedOutDoesNotWriteVerdictWhenEvidenceTimesOut(t *testing.T) {
	surface, fixture, closeSocket := specializedTimeoutSurface(t, false)
	defer closeSocket()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/sessions/s"):
			_, _ = w.Write(surface)
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages"):
			<-req.Context().Done()
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()

	r := result{ToolCalls: map[string]int{}, AgentID: "a", SessionID: "s", BridgeNonce: "nonce", MCPRegistrationID: "registration", MCPTools: []string{specializedFixtureRegistrationName}}
	code := finishTimedOut(t.Context(), time.Now().Add(50*time.Millisecond), apiClient{baseURL: server.URL, http: server.Client()}, &r, "", func(*int64) {}, taskSkillBashGuard, &fixture, "lease", nil)
	if code != exitAdapter || r.HostVerdict != nil || r.FailureClass != "adapter" {
		t.Fatalf("timeout result = code %d class %q verdict=%+v, want invalid without verdict", code, r.FailureClass, r.HostVerdict)
	}
}

func TestRunWritesTypedResultBeforeTheTimeoutFinalizationWall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/status":
			_, _ = w.Write([]byte(`{"sandbox_backend":"bridge"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"provisioned_user":{"id":"provisioned"},"token":"trial"}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/auth/me":
			_, _ = w.Write([]byte(`{"id":"account"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/agents":
			_, _ = w.Write([]byte(`{"id":"agent"}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/agents/agent/tools":
			_, _ = w.Write([]byte(`{"tools":[{"name":"bash","source":"core","enabled":true}]}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/agents/agent/sessions":
			_, _ = w.Write([]byte(`{"id":"session"}`))
		case req.Method == http.MethodPost && req.URL.Path == "/api/agents/agent/sessions/session/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-req.Context().Done()
		case req.Method == http.MethodPost && req.URL.Path == "/api/agents/agent/sessions/session/stop":
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && req.URL.Path == "/api/agents/agent/sessions/session":
			_, _ = w.Write([]byte(`{"activity_status":"stopped"}`))
		case req.Method == http.MethodGet && req.URL.Path == "/api/agents/agent/sessions/session/messages":
			<-req.Context().Done()
		case req.Method == http.MethodDelete && req.URL.Path == "/api/agents/agent":
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && req.URL.Path == "/api/provisioned-users/provisioned/deactivate":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	instruction := filepath.Join(dir, "instruction.txt")
	if err := os.WriteFile(instruction, []byte("do work"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(binding, []byte(`{"socket":"/tmp/bridge.sock","nonce":"nonce","workdir":"/app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "result.json")
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	})
	t.Setenv("STELLA_EVAL_ADMIN_TOKEN", "admin")
	os.Args = []string{
		"stella-eval-agent", "--stella-url", server.URL, "--instruction-file", instruction,
		"--binding-template", binding, "--binding-dir", filepath.Join(dir, "bindings"), "--model", "p/m",
		"--user-id", "trial", "--finalize-by-unix-ms", fmt.Sprint(time.Now().Add(2 * time.Second).UnixMilli()),
		"--finalization-budget-seconds", "1", "--output", output,
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	started := time.Now()
	if code := run(); code != exitAdapter {
		t.Fatalf("run exit code = %d, want typed adapter invalid", code)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("driver exceeded its absolute finalize-by deadline: %s", elapsed)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.TimedOut || got.FailureClass != "adapter" || got.HostVerdict != nil {
		t.Fatalf("result = %+v, want timeout adapter-invalid with no verdict", got)
	}
}
