package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
)

// NewRunnerFactory creates a NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. Runner execution is always user-scoped,
// so per-user workspace directories are created for every runner instance.
//
// Hooks are not part of the factory — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
//
// TODO: consolidate the 14 positional parameters into a RunnerFactoryConfig struct.
func NewRunnerFactory(snap *config.Snapshot, builtinTools []tools.Tool, pluginToolsBuilder PluginToolsBuilder, providerRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error), promptToolsFn func(context.Context) ([]pkgplugins.PromptToolInfo, error), promptSectionsFn func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error), pluginPromptsFn func() []pkgplugins.SystemPromptSection, sessionPluginViewFn SessionPluginViewBuilder, toolLifecycle *coreagent.ToolLifecycle, skillStore pkgplugins.SkillStore, sandboxBackendFn func(ctx context.Context) string, vaultEnvLoader VaultEnvLoader, tokenService *auth.TokenService, tokenManager *oauth.TokenManager) (NewRunnerFunc, error) {
	switch snap.Runner.Type {
	case "go":
		return func(ctx context.Context, params RunnerParams) (Runner, error) {
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
			pluginView := pkgplugins.SessionPluginView{}
			if sessionPluginViewFn != nil {
				pluginView, _ = sessionPluginViewFn(ctx)
			}
			promptBuild := pkgplugins.SystemPromptContext{
				AnnaHome:            config.AnnaHome(),
				HomeDir:             homeDir,
				AgentRoot:           snap.Workspace,
				ProjectRoot:         "",
				UserID:              params.UserID,
				AgentID:             params.AgentID,
				UserRoot:            userRoot,
				RegisteredPluginIDs: append([]string(nil), pluginView.RegisteredPluginIDs...),
				EnabledPluginIDs:    append([]string(nil), pluginView.EnabledPluginIDs...),
			}
			var promptSections []pkgplugins.SystemPromptSection
			if promptSectionsFn != nil {
				promptSections, _ = promptSectionsFn(ctx, promptBuild)
			}
			if skillsSection, err := skillstool.BuildPromptSection(ctx, promptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
				promptSections = append(promptSections, skillsSection)
			}

			var pluginPrompts []pkgplugins.SystemPromptSection
			if pluginPromptsFn != nil {
				pluginPrompts = pluginPromptsFn()
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := BuildSystemPromptFromDB(ctx, DBPromptParams{
				SystemPrompt:   snap.SystemPrompt,
				AgentSoul:      snap.Soul,
				Memory:         memProvider,
				UserID:         params.UserID,
				AgentID:        params.AgentID,
				AnnaHome:       config.AnnaHome(),
				AgentRoot:      snap.Workspace,
				UserRoot:       userRoot,
				PromptTools:    promptTools,
				PluginPrompts:  pluginPrompts,
				PromptSections: promptSections,
			})

			// Resolve hooks from RunnerParams — injected by Pool, not the factory.
			var hookPlugins []hooks.HookPlugin
			if params.HooksFn != nil {
				hookPlugins = params.HooksFn()
			}

			runnerTools := append([]tools.Tool{}, builtinTools...)
			runnerTools = append(runnerTools, skillstool.NewTool(
				skillStore,
				config.AnnaHome(),
				snap.Workspace,
				"",
				filepath.Join(userRoot, ".agents", "skills"),
			))

			return NewGoRunner(ctx, GoRunnerConfig{
				API:              apiName,
				Model:            modelID,
				APIKey:           creds.APIKey,
				AgentRoot:        snap.Workspace,
				AnnaHome:         config.AnnaHome(),
				BaseURL:          creds.BaseURL,
				System:           system,
				PluginPrompts:    pluginPrompts,
				PromptSections:   promptSections,
				ExtraTools:       runnerTools,
				PluginTools:      pluginToolsBuilder,
				SessionEnvSpecs:  append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
				UserRoot:         userRoot,
				Sandbox:          snap.Sandbox,
				SandboxBackendFn: sandboxBackendFn,
				HookPlugins:      hookPlugins,
				ToolLifecycle:    toolLifecycle,
				Providers:        providerRegistryBuilder,
				UserID:           params.UserID,
				AgentID:          params.AgentID,
				VaultEnvLoader:   vaultEnvLoader,
				TokenService:     tokenService,
				TokenManager:     tokenManager,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", snap.Runner.Type)
	}
}
