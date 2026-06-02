package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	"github.com/CherryHQ/stella/internal/config"
)

// resolveAgentID runs a throwaway command so taskAgentID gets a real context
// with the --agent-id flag wired up, then returns its result.
func resolveAgentID(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var got string
	var gotErr error
	app := ucli.NewApp()
	app.Writer = &bytes.Buffer{}
	app.Commands = []*ucli.Command{{
		Name:  "probe",
		Flags: []ucli.Flag{&ucli.StringFlag{Name: "agent-id"}},
		Action: func(c *ucli.Context) error {
			got, gotErr = taskAgentID(c)
			return nil
		},
	}}
	if err := app.Run(append([]string{"stella", "probe"}, args...)); err != nil {
		t.Fatalf("run probe: %v", err)
	}
	return got, gotErr
}

func TestTaskAgentIDPrecedence(t *testing.T) {
	t.Setenv("STELLA_AGENT_ID", "from-env")

	// Flag wins over env.
	if got, err := resolveAgentID(t, "--agent-id", "from-flag"); err != nil || got != "from-flag" {
		t.Fatalf("flag should win: got %q err %v", got, err)
	}
	// Env fills when flag absent.
	if got, err := resolveAgentID(t); err != nil || got != "from-env" {
		t.Fatalf("env should fill: got %q err %v", got, err)
	}
	// Neither set errors clearly.
	t.Setenv("STELLA_AGENT_ID", "")
	got, err := resolveAgentID(t)
	if err == nil {
		t.Fatalf("expected error when neither flag nor env set, got %q", got)
	}
	if !strings.Contains(err.Error(), "--agent-id") || !strings.Contains(err.Error(), "STELLA_AGENT_ID") {
		t.Fatalf("error should name both sources, got %q", err)
	}
}

func TestAPIClientRequiresToken(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "")
	_, err := apiclient.NewAPIClient()
	if err == nil {
		t.Fatal("expected error when STELLA_TOKEN unset")
	}
	if !strings.Contains(err.Error(), "STELLA_TOKEN") {
		t.Fatalf("error should mention STELLA_TOKEN, got %q", err)
	}
}

func TestServerURLPrecedence(t *testing.T) {
	t.Setenv("STELLA_SERVER_URL", "https://example.test:9000")
	if got := config.ServerURL(); got != "https://example.test:9000" {
		t.Fatalf("env should control base URL, got %q", got)
	}
	t.Setenv("STELLA_SERVER_URL", "")
	if got := config.ServerURL(); got != "http://127.0.0.1:25678" {
		t.Fatalf("default base URL expected when unset, got %q", got)
	}
}

func TestDotEnvDoesNotOverrideProcessEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("DOTENV_ONLY=from-file\nDOTENV_EXISTING=from-file\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("STELLA_HOME", dir)
	t.Setenv("DOTENV_EXISTING", "from-process")
	t.Setenv("DOTENV_ONLY", "")

	loadDotEnv()

	if got := os.Getenv("DOTENV_EXISTING"); got != "from-process" {
		t.Fatalf("process env must win over .env, got %q", got)
	}
	if got := os.Getenv("DOTENV_ONLY"); got != "from-file" {
		t.Fatalf(".env should fill missing value, got %q", got)
	}
}

func TestFailingCommandKeepsStdoutClean(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "")
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	app := ucli.NewApp()
	app.Writer = out
	app.ErrWriter = errOut
	app.Commands = []*ucli.Command{vaultCommand()}
	err := app.Run([]string{"stella", "vault", "list"})
	if err == nil {
		t.Fatal("expected error from API-backed command without token")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay clean on error, got %q", out.String())
	}
}
