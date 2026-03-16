package agent

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/agent/runner"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/config"
)

// NewRunnerFactory creates a runner.NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. When the snapshot includes a SystemPrompt
// (from DB), BuildSystemPromptFromDB is used; otherwise falls back to
// BuildSystemPrompt for backward compatibility.
func NewRunnerFactory(snap *config.Snapshot, extraTools []agenttool.Tool, pluginHooks engine.PluginHookRunner) (runner.NewRunnerFunc, error) {
	switch snap.Runner.Type {
	case "go":
		// Pre-build the system prompt so every runner created by this factory
		// shares the same prompt (it only changes when the factory is replaced).
		system := buildSystemPrompt(snap)

		return func(ctx context.Context, model string) (runner.Runner, error) {
			if model == "" {
				model = snap.Model
			}
			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:         snap.Provider,
				Model:       model,
				APIKey:      snap.APIKey,
				Workspace:   snap.Workspace,
				AnnaHome:    config.AnnaHome(),
				BaseURL:     snap.BaseURL,
				System:      system,
				ExtraTools:  extraTools,
				PluginHooks: pluginHooks,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}

// buildSystemPrompt selects the prompt-building strategy based on whether the
// snapshot carries a DB-sourced SystemPrompt.
func buildSystemPrompt(snap *config.Snapshot) string {
	if snap.SystemPrompt != "" {
		return runner.BuildSystemPromptFromDB(runner.DBPromptParams{
			SystemPrompt: snap.SystemPrompt,
			AnnaHome:     config.AnnaHome(),
			Workspace:    snap.Workspace,
		})
	}
	// Fallback: legacy file-based prompt for agents without a DB system prompt.
	return ""
}
