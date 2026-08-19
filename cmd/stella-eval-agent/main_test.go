package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
}
