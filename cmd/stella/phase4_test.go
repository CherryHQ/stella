package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
)

func runApp(t *testing.T, cmd *ucli.Command, args ...string) (string, string) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	app := ucli.NewApp()
	app.Writer = out
	app.ErrWriter = errOut
	app.Commands = []*ucli.Command{cmd}
	if err := app.Run(append([]string{"stella"}, args...)); err != nil {
		t.Fatalf("Run %v: %v", args, err)
	}
	return out.String(), errOut.String()
}

func phase4Server(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("STELLA_TOKEN", "test-token")
	t.Setenv("STELLA_AGENT_ID", "agent-1")
	t.Setenv("STELLA_SESSION_ID", "session-1")
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
}

func TestSkillCommandHasNoLegacyAliases(t *testing.T) {
	cmd := skillsCommand()
	if len(cmd.Aliases) != 0 {
		t.Fatalf("expected no top-level skill aliases, got %v", cmd.Aliases)
	}
	for _, sub := range cmd.Subcommands {
		if sub.Name == "install" && len(sub.Aliases) != 0 {
			t.Fatalf("expected install to have no aliases, got %v", sub.Aliases)
		}
	}
}

func TestSkillSearchHelpUsesCanonicalCommand(t *testing.T) {
	out := &bytes.Buffer{}
	app := ucli.NewApp()
	app.Writer = out
	app.Commands = []*ucli.Command{skillsCommand()}
	if err := app.Run([]string{"stella", "skill", "search", "--help"}); err != nil {
		t.Fatalf("Run help: %v", err)
	}
	legacyPlural := "stella " + "skills"
	if strings.Contains(out.String(), legacyPlural) {
		t.Fatalf("help contains legacy plural command: %q", out.String())
	}
	if !strings.Contains(out.String(), "stella skill search react") {
		t.Fatalf("help missing canonical example: %q", out.String())
	}
}

func TestSchedulerListJSONEnvelope(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[{"id":"job-1","name":"daily","session_mode":"reuse","every":"24h"}]}`))
	})
	out, _ := runApp(t, schedulerCommand(), "scheduler", "list", "--json")
	var env struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output not JSON envelope: %v (%q)", err, out)
	}
	if len(env.Jobs) != 1 || env.Jobs[0].ID != "job-1" {
		t.Fatalf("got %+v", env)
	}
}

func TestSchedulerRemoveJSON(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out, _ := runApp(t, schedulerCommand(), "scheduler", "remove", "--json", "job-1")
	var res deletedResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if res.ID != "job-1" || !res.Deleted {
		t.Fatalf("got %+v", res)
	}
}

func TestVaultListNeverLeaksValues(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[{"name":"openai","created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z"}]}`))
	})
	out, _ := runApp(t, vaultCommand(), "vault", "list", "--json")
	if strings.Contains(out, "value") {
		t.Fatalf("vault list output must not include secret values: %q", out)
	}
	if !strings.Contains(out, "openai") {
		t.Fatalf("expected entry name in output: %q", out)
	}
}

func TestVaultDeleteJSON(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out, _ := runApp(t, vaultCommand(), "vault", "delete", "--json", "openai")
	var res deletedResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if res.ID != "openai" || !res.Deleted {
		t.Fatalf("got %+v", res)
	}
}

func TestShareArtifactURLToStdout(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sh-1","url":"https://share.example/sh-1","source":"artifact"}`))
	})
	out, _ := runApp(t, shareCommand(), "share", "artifact", "report.md")
	if strings.TrimSpace(out) != "https://share.example/sh-1" {
		t.Fatalf("default share output should be the URL, got %q", out)
	}
}

func TestShareArtifactJSON(t *testing.T) {
	phase4Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sh-1","url":"https://share.example/sh-1","source":"artifact"}`))
	})
	out, _ := runApp(t, shareCommand(), "share", "artifact", "--json", "report.md")
	var res struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if res.ID != "sh-1" {
		t.Fatalf("got %+v", res)
	}
}
