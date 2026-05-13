package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
	skillstool "github.com/CherryHQ/stella/plugins/tools/skills"
)

// RunnerFactoryConfig holds all dependencies needed to create a NewRunnerFunc.
type RunnerFactoryConfig struct {
	Snap                     *config.Snapshot
	BuiltinTools             []tools.Tool
	PluginToolsBuilder       PluginToolsBuilder
	ProviderRegistryBuilder  ProviderRegistryBuilder
	PromptToolsBuilder       PromptToolsBuilder
	PromptSectionsBuilder    PromptSectionsBuilder
	PluginPromptsBuilder     PluginPromptsBuilder
	SessionPluginViewBuilder SessionPluginViewBuilder
	ToolLifecycle            *coreagent.ToolLifecycle
	SkillStore               pkgplugins.SkillStore
	SandboxBackendFn         func(ctx context.Context) string
	VaultEnvLoader           VaultEnvLoader
	TokenService             *auth.TokenService
	TokenManager             *oauth.TokenManager
}

// NewRunnerFactory creates a NewRunnerFunc for a given config snapshot.
// The returned factory creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. Runner execution is always user-scoped,
// so per-user workspace directories are created for every runner instance.
//
// Hooks are not part of the factory — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
func NewRunnerFactory(cfg RunnerFactoryConfig) (NewRunnerFunc, error) {
	switch cfg.Snap.Runner.Type {
	case "go":
		return func(ctx context.Context, params RunnerParams) (Runner, error) {
			modelRef := params.Model
			if modelRef == "" {
				modelRef = cfg.Snap.Model
			}

			// Parse provider/model from the ref string.
			provID, modelID := config.ParseModelRef(modelRef)
			if provID == "" {
				provID = cfg.Snap.Provider
			}
			creds := cfg.Snap.ResolveProviderCreds(provID)
			apiName := creds.Type
			if apiName == "" {
				apiName = provID
			}

			var (
				userDir string
				err     error
			)
			if params.UserID > 0 {
				userDir, err = SetupUserWorkspace(cfg.Snap.AgentID, config.StellaHome(), params.UserID)
			} else {
				userDir, err = SetupSystemWorkspace(cfg.Snap.AgentID, config.StellaHome())
			}
			if err != nil {
				return nil, fmt.Errorf("setup workspace: %w", err)
			}
			userRoot := userDir

			// Extract memory provider from params (typed as any to avoid circular imports).
			var memProvider memory.Provider
			if params.Memory != nil {
				memProvider, _ = params.Memory.(memory.Provider)
			}

			var promptTools []pkgplugins.PromptToolInfo
			if cfg.PromptToolsBuilder != nil {
				promptTools, _ = cfg.PromptToolsBuilder(ctx)
			}
			homeDir, _ := os.UserHomeDir()
			pluginView := pkgplugins.SessionPluginView{}
			if cfg.SessionPluginViewBuilder != nil {
				pluginView, _ = cfg.SessionPluginViewBuilder(ctx)
			}
			promptBuild := pkgplugins.SystemPromptContext{
				StellaHome:          config.StellaHome(),
				HomeDir:             homeDir,
				AgentRoot:           cfg.Snap.Workspace,
				ProjectRoot:         "",
				UserID:              params.UserID,
				AgentID:             params.AgentID,
				UserRoot:            userRoot,
				RegisteredPluginIDs: append([]string(nil), pluginView.RegisteredPluginIDs...),
				EnabledPluginIDs:    append([]string(nil), pluginView.EnabledPluginIDs...),
			}
			var promptSections []pkgplugins.SystemPromptSection
			if cfg.PromptSectionsBuilder != nil {
				promptSections, _ = cfg.PromptSectionsBuilder(ctx, promptBuild)
			}
			if skillsSection, err := skillstool.BuildPromptSection(ctx, promptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
				promptSections = append(promptSections, skillsSection)
			}

			var pluginPrompts []pkgplugins.SystemPromptSection
			if cfg.PluginPromptsBuilder != nil {
				pluginPrompts = cfg.PluginPromptsBuilder()
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
				SystemPrompt:   cfg.Snap.SystemPrompt,
				AgentSoul:      cfg.Snap.Soul,
				Memory:         memProvider,
				UserID:         params.UserID,
				AgentID:        params.AgentID,
				StellaHome:     config.StellaHome(),
				AgentRoot:      cfg.Snap.Workspace,
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

			runnerTools := append([]tools.Tool{}, cfg.BuiltinTools...)
			runnerTools = append(runnerTools, skillstool.NewTool(
				cfg.SkillStore,
				config.StellaHome(),
				cfg.Snap.Workspace,
				"",
				filepath.Join(userRoot, ".agents", "skills"),
			))

			return NewGoRunner(ctx, GoRunnerConfig{
				API:              apiName,
				Model:            modelID,
				APIKey:           creds.APIKey,
				AgentRoot:        cfg.Snap.Workspace,
				StellaHome:       config.StellaHome(),
				BaseURL:          creds.BaseURL,
				System:           system,
				PluginPrompts:    pluginPrompts,
				PromptSections:   promptSections,
				ExtraTools:       runnerTools,
				PluginTools:      cfg.PluginToolsBuilder,
				SessionEnvSpecs:  append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
				UserRoot:         userRoot,
				Sandbox:          cfg.Snap.Sandbox,
				SandboxBackendFn: cfg.SandboxBackendFn,
				HookPlugins:      hookPlugins,
				ToolLifecycle:    cfg.ToolLifecycle,
				Providers:        cfg.ProviderRegistryBuilder,
				UserID:           params.UserID,
				VaultEnvLoader:   cfg.VaultEnvLoader,
				TokenService:     cfg.TokenService,
				TokenManager:     cfg.TokenManager,
				SubagentTimeout:  cfg.Snap.Runner.SubagentTimeoutDuration(),
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", cfg.Snap.Runner.Type)
	}
}
