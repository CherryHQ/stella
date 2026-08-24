package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
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
	cleanupTrialResources(t.Context(), &first, user, admin, "provisioned")
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
	cleanupTrialResources(t.Context(), &second, user, admin, "provisioned")
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
	code := finishTimedOut(apiClient{baseURL: server.URL, http: server.Client()}, &r, trajectory, func(*int64) {}, stopConfirmBudget, "")
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
