package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func failClosedConfig(t *testing.T) runnerConfig {
	t.Helper()
	home := t.TempDir()
	return runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "user-1"),
		}},
		BuiltinParams:       RunnerParams{UserID: "user-1", AgentID: "agent-1"},
		SkillRevisionReader: emptySkillRuntime{},
		SkillReadAuthorizer: allowSkillReads{},
	}
}

// captureLogs redirects slog for the duration of the test and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// An availability check that cannot answer must not be read as "unavailable":
// that would quietly strip a capability for the whole life of the runner.
func TestBuildToolRegistryFailsWhenAvailabilityIsUnknown(t *testing.T) {
	cfg := failClosedConfig(t)
	probeErr := errors.New("vault unreachable")
	cfg.BuiltinTools = []BuiltinTool{{
		Tool: staticTool{name: "email"},
		Available: func(context.Context, RunnerParams) (bool, error) {
			return false, probeErr
		},
	}}

	_, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err == nil {
		t.Fatal("expected the runner build to fail while availability is unknown")
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("error should wrap the availability failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("error should name the tool, got %v", err)
	}
}

// The inverse of the fail-open bug this replaces: a failed override fetch used
// to leave every tool visible, including ones an administrator had disabled.
func TestBuildToolRegistryFailsWhenOverrideFetchFails(t *testing.T) {
	cfg := failClosedConfig(t)
	fetchErr := errors.New("database unreachable")
	cfg.BuiltinTools = []BuiltinTool{{Tool: staticTool{name: "memory"}}}
	cfg.ToolOverrideFetcher = func(context.Context, string, string) ([]ToolOverride, error) {
		return nil, fetchErr
	}

	_, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err == nil {
		t.Fatal("expected the runner build to fail when overrides cannot be loaded")
	}
	if !errors.Is(err, fetchErr) {
		t.Fatalf("error should wrap the fetch failure, got %v", err)
	}
}

// Two tools under one name means model calls reach an implementation nobody
// chose. Fail the build and name both sides so the collision is fixable.
func TestBuildToolRegistryRejectsDuplicateNonCoreName(t *testing.T) {
	cfg := failClosedConfig(t)
	cfg.BuiltinTools = []BuiltinTool{{Tool: staticTool{name: "share"}}}
	cfg.PerRunTools = []pkgtools.Tool{staticTool{name: "share"}}

	_, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err == nil {
		t.Fatal("expected the runner build to fail on a duplicate tool name")
	}
	for _, want := range []string{"share", toolSourceBuiltin, toolSourcePerRun} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q, got %v", want, err)
		}
	}
}

// A row left behind by a renamed or removed tool is stale data. Warn, and keep
// building: failing here would lock the user out of a working agent.
func TestBuildToolRegistryWarnsOnOrphanOverride(t *testing.T) {
	logs := captureLogs(t)
	cfg := failClosedConfig(t)
	cfg.BuiltinTools = []BuiltinTool{
		{Tool: staticTool{name: "memory"}},
		{
			Tool:      staticTool{name: "email"},
			Available: func(context.Context, RunnerParams) (bool, error) { return false, nil },
		},
	}
	cfg.ToolOverrideFetcher = func(context.Context, string, string) ([]ToolOverride, error) {
		return []ToolOverride{
			{ToolName: "recally_digest", Scope: ToolOverrideScopeUser, Enabled: false},
			{ToolName: "memory", Scope: ToolOverrideScopeUser, Enabled: false},
			{ToolName: "email", Scope: ToolOverrideScopeUser, Enabled: false},
			{ToolName: "bash", Scope: ToolOverrideScopeUser, Enabled: false},
		}, nil
	}

	reg, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	if reg.Has("memory") {
		t.Fatal("the override should still have disabled memory")
	}
	if !strings.Contains(logs.String(), "recally_digest") {
		t.Fatalf("orphan override row should be logged, got %s", logs.String())
	}
	// A disabled tool, a tool this run cannot use, and a core name are all known
	// names: none of them is a stale row.
	for _, quiet := range []string{"memory", "email", "bash"} {
		if strings.Contains(logs.String(), "tool="+quiet) {
			t.Fatalf("%s is a known tool and must not be reported as unknown: %s", quiet, logs.String())
		}
	}
}

// Per-run exclusion lists come from callers with their own copy of the tool
// names; a name that has since gone away hides nothing and must not fail the run.
func TestFilterRunnerToolsWarnsOnUnknownExclusion(t *testing.T) {
	logs := captureLogs(t)
	reg := pkgtools.NewRegistry()
	if err := reg.Register(staticTool{name: "goal"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, defs, err := filterRunnerTools(reg, []string{"goal_list"})
	if err != nil {
		t.Fatalf("filterRunnerTools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "goal" {
		t.Fatalf("defs = %#v, want the registered goal tool", defs)
	}
	if !strings.Contains(logs.String(), "goal_list") {
		t.Fatalf("unknown exclusion should be logged, got %s", logs.String())
	}
}
