package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/agent/runner"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/skills"
)

// NewRunnerFactory creates a runner.NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. User memory and user ID are injected per-session
// from RunnerParams. When UserID > 0, per-user workspace directories are set up
// and per-user skills tools are created.
func NewRunnerFactory(snap *config.Snapshot, extraTools []agenttool.Tool, pluginHooks engine.PluginHookRunner) (runner.NewRunnerFunc, error) {
	switch snap.Runner.Type {
	case "go":
		return func(ctx context.Context, params runner.RunnerParams) (runner.Runner, error) {
			modelRef := params.Model
			if modelRef == "" {
				modelRef = snap.Model
			}

			// Parse provider/model from the ref string.
			provID, modelID := config.ParseModelRef(modelRef)
			if provID == "" {
				provID = snap.Provider
			}
			creds := snap.ResolveProviderCreds(provID)

			// Determine per-user paths when user ID is set.
			var userSkillsDir string
			var userDataDir string
			if params.UserID > 0 {
				userDir, err := SetupUserWorkspace(snap.AgentID, config.AnnaHome(), params.UserID)
				if err == nil {
					userSkillsDir = UserSkillsDir(userDir)
					userDataDir = UserDataDir(userDir)
				}
			}

			// Build the full system prompt per-session with user memory and user skills.
			system := runner.BuildSystemPromptFromDB(runner.DBPromptParams{
				SystemPrompt:  snap.SystemPrompt,
				UserMemory:    params.UserMemory,
				AnnaHome:      config.AnnaHome(),
				Workspace:     snap.Workspace,
				UserSkillsDir: userSkillsDir,
			})

			// Build per-session extra tools.
			// Replace any SkillsTool with a per-user one when UserID > 0.
			sessionTools := buildSessionTools(extraTools, snap.Workspace, userSkillsDir, userDataDir, params.UserID)

			// Use user data dir as working dir when available, otherwise use system cwd.
			workDir := userDataDir

			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:         provID,
				Model:       modelID,
				APIKey:      creds.APIKey,
				Workspace:   snap.Workspace,
				AnnaHome:    config.AnnaHome(),
				BaseURL:     creds.BaseURL,
				System:      system,
				ExtraTools:  sessionTools,
				PluginHooks: pluginHooks,
				WorkDir:     workDir,
				UserDataDir: userDataDir,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}

// buildSessionTools creates per-session tools from the template extra tools.
// When userID > 0, the SkillsTool is replaced with a per-user version,
// and sandbox-aware file tools are configured.
func buildSessionTools(templateTools []agenttool.Tool, workspace, userSkillsDir, userDataDir string, userID int64) []agenttool.Tool {
	if userID <= 0 {
		return templateTools
	}

	result := make([]agenttool.Tool, 0, len(templateTools))
	for _, t := range templateTools {
		// Replace SkillsTool with per-user version.
		if _, ok := t.(*skills.SkillsTool); ok {
			cwd, _ := os.Getwd()
			result = append(result, skills.NewTool(config.AnnaHome(), workspace, cwd, userID))
			continue
		}
		result = append(result, t)
	}
	return result
}
