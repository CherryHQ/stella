package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
)

// NewRunnerFactory creates a runner.NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. When UserID > 0, per-user workspace
// directories are set up and per-user skills tools are created.
//
// Hooks are not part of the factory — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
func NewRunnerFactory(snap *config.Snapshot, extraTools []tools.Tool, providerRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error), promptToolsFn func(context.Context) ([]pkgplugins.PromptToolInfo, error)) (runner.NewRunnerFunc, error) {
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

			// Extract memory provider from params (typed as any to avoid circular imports).
			var memProvider memory.Provider
			if params.Memory != nil {
				memProvider, _ = params.Memory.(memory.Provider)
			}

			var promptTools []pkgplugins.PromptToolInfo
			if promptToolsFn != nil {
				promptTools, _ = promptToolsFn(ctx)
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := runner.BuildSystemPromptFromDB(ctx, runner.DBPromptParams{
				SystemPrompt:  snap.SystemPrompt,
				Memory:        memProvider,
				UserID:        params.UserID,
				AgentID:       params.AgentID,
				AnnaHome:      config.AnnaHome(),
				Workspace:     snap.Workspace,
				UserSkillsDir: userSkillsDir,
				PromptTools:   promptTools,
			})

			// Build per-session extra tools.
			// Replace any SkillsTool with a per-user one when UserID > 0.
			sessionTools := buildSessionTools(extraTools, snap.Workspace, userSkillsDir, userDataDir, params.UserID)

			// Use user data dir as working dir when available, otherwise use system cwd.
			workDir := userDataDir

			// Resolve hooks from RunnerParams — injected by Pool, not the factory.
			var hookPlugins []hooks.HookPlugin
			if params.HooksFn != nil {
				hookPlugins = params.HooksFn()
			}

			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:         provID,
				Model:       modelID,
				APIKey:      creds.APIKey,
				Workspace:   snap.Workspace,
				AnnaHome:    config.AnnaHome(),
				BaseURL:     creds.BaseURL,
				System:      system,
				ExtraTools:  sessionTools,
				WorkDir:     workDir,
				UserDataDir: userDataDir,
				HookPlugins: hookPlugins,
				Providers:   providerRegistryBuilder,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}

// buildSessionTools creates per-session tools from the template extra tools.
// When userID > 0, the SkillsTool is replaced with a per-user version,
// and sandbox-aware file tools are configured.
func buildSessionTools(templateTools []tools.Tool, workspace, userSkillsDir, userDataDir string, userID int64) []tools.Tool {
	if userID <= 0 {
		return templateTools
	}

	result := make([]tools.Tool, 0, len(templateTools))
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
