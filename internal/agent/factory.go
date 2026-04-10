package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	coreagent "github.com/vaayne/anna/pkg/agent"
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
func NewRunnerFactory(snap *config.Snapshot, extraTools []tools.Tool, coreToolsBuilder runner.CoreToolsBuilder, providerRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error), promptToolsFn func(context.Context) ([]pkgplugins.PromptToolInfo, error), promptSectionsFn func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error), toolLifecycle *coreagent.ToolLifecycle) (runner.NewRunnerFunc, error) {
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
			apiName := creds.Type
			if apiName == "" {
				apiName = provID
			}
			cwd, _ := os.Getwd()

			// Determine per-user paths when user ID is set.
			var userDataDir string
			if params.UserID > 0 {
				userDir, err := SetupUserWorkspace(snap.AgentID, config.AnnaHome(), params.UserID)
				if err == nil {
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
			var promptSections []pkgplugins.SystemPromptSection
			if promptSectionsFn != nil {
				promptSections, _ = promptSectionsFn(ctx, pkgplugins.SystemPromptContext{
					AnnaHome:    config.AnnaHome(),
					Workspace:   snap.Workspace,
					Cwd:         cwd,
					UserID:      params.UserID,
					AgentID:     params.AgentID,
					UserDataDir: userDataDir,
				})
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := runner.BuildSystemPromptFromDB(ctx, runner.DBPromptParams{
				SystemPrompt:   snap.SystemPrompt,
				Memory:         memProvider,
				UserID:         params.UserID,
				AgentID:        params.AgentID,
				AnnaHome:       config.AnnaHome(),
				Workspace:      snap.Workspace,
				Cwd:            cwd,
				UserDataDir:    userDataDir,
				PromptTools:    promptTools,
				PromptSections: promptSections,
			})

			// Use user data dir as working dir when available, otherwise use system cwd.
			workDir := userDataDir

			// Resolve hooks from RunnerParams — injected by Pool, not the factory.
			var hookPlugins []hooks.HookPlugin
			if params.HooksFn != nil {
				hookPlugins = params.HooksFn()
			}

			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:            apiName,
				Model:          modelID,
				APIKey:         creds.APIKey,
				Workspace:      snap.Workspace,
				AnnaHome:       config.AnnaHome(),
				BaseURL:        creds.BaseURL,
				System:         system,
				PromptSections: promptSections,
				ExtraTools:     extraTools,
				WorkDir:        workDir,
				UserDataDir:    userDataDir,
				HookPlugins:    hookPlugins,
				ToolLifecycle:  toolLifecycle,
				CoreTools:      coreToolsBuilder,
				Providers:      providerRegistryBuilder,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}
