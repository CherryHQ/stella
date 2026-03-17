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
// workspace, and system prompt. User memory is injected per-session from
// RunnerParams.UserMemory.
func NewRunnerFactory(snap *config.Snapshot, extraTools []agenttool.Tool, pluginHooks engine.PluginHookRunner) (runner.NewRunnerFunc, error) {
	switch snap.Runner.Type {
	case "go":
		return func(ctx context.Context, params runner.RunnerParams) (runner.Runner, error) {
			model := params.Model
			if model == "" {
				model = snap.Model
			}

			// Build the full system prompt per-session with user memory.
			system := runner.BuildSystemPromptFromDB(runner.DBPromptParams{
				SystemPrompt: snap.SystemPrompt,
				UserMemory:   params.UserMemory,
				AnnaHome:     config.AnnaHome(),
				Workspace:    snap.Workspace,
			})

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
