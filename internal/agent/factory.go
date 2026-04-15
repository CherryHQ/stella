package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
)

// NewRunnerFactory creates a runner.NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. Runner execution is always user-scoped,
// so per-user workspace directories are created for every runner instance.
//
// Hooks are not part of the factory — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
func NewRunnerFactory(snap *config.Snapshot, extraTools []tools.Tool, pluginToolsBuilder PluginToolsBuilder, providerRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error), promptToolsFn func(context.Context) ([]pkgplugins.PromptToolInfo, error), promptSectionsFn func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error), toolLifecycle *coreagent.ToolLifecycle) (runner.NewRunnerFunc, error) {
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

			if params.UserID <= 0 {
				return nil, fmt.Errorf("runner requires user scope")
			}
			userDir, err := SetupUserWorkspace(snap.AgentID, config.AnnaHome(), params.UserID)
			if err != nil {
				return nil, fmt.Errorf("setup user workspace: %w", err)
			}
			userRoot := UserRoot(userDir)

			// Extract memory provider from params (typed as any to avoid circular imports).
			var memProvider memory.Provider
			if params.Memory != nil {
				memProvider, _ = params.Memory.(memory.Provider)
			}

			var promptTools []pkgplugins.PromptToolInfo
			if promptToolsFn != nil {
				promptTools, _ = promptToolsFn(ctx)
			}
			homeDir, _ := os.UserHomeDir()
			promptBuild := pkgplugins.SystemPromptContext{
				AnnaHome:    config.AnnaHome(),
				HomeDir:     homeDir,
				AgentRoot:   snap.Workspace,
				ProjectRoot: "",
				UserID:      params.UserID,
				AgentID:     params.AgentID,
				UserRoot:    userRoot,
			}
			var promptSections []pkgplugins.SystemPromptSection
			if promptSectionsFn != nil {
				promptSections, _ = promptSectionsFn(ctx, promptBuild)
			}
			if skillsSection, err := skillstool.BuildPromptSection(ctx, promptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
				promptSections = append(promptSections, skillsSection)
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := runner.BuildSystemPromptFromDB(ctx, runner.DBPromptParams{
				SystemPrompt:   snap.SystemPrompt,
				Memory:         memProvider,
				UserID:         params.UserID,
				AgentID:        params.AgentID,
				AnnaHome:       config.AnnaHome(),
				AgentRoot:      snap.Workspace,
				UserRoot:       userRoot,
				PromptTools:    promptTools,
				PromptSections: promptSections,
			})

			// Resolve hooks from RunnerParams — injected by Pool, not the factory.
			var hookPlugins []hooks.HookPlugin
			if params.HooksFn != nil {
				hookPlugins = params.HooksFn()
			}

			runnerTools := append([]tools.Tool{}, extraTools...)
			runnerTools = append(runnerTools, skillstool.NewTool(
				config.AnnaHome(),
				snap.Workspace,
				"",
				filepath.Join(userRoot, ".agents", "skills"),
				nil,
			))

			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:            apiName,
				Model:          modelID,
				APIKey:         creds.APIKey,
				AgentRoot:      snap.Workspace,
				AnnaHome:       config.AnnaHome(),
				BaseURL:        creds.BaseURL,
				System:         system,
				PromptSections: promptSections,
				ExtraTools:     runnerTools,
				PluginTools:    pluginToolsBuilder,
				UserRoot:       userRoot,
				Sandbox:        snap.Sandbox,
				HookPlugins:    hookPlugins,
				ToolLifecycle:  toolLifecycle,
				Providers:      providerRegistryBuilder,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}
