package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// RunnerFactoryConfig holds all dependencies needed to create a NewRunnerFunc.
type RunnerFactoryConfig struct {
	Snap                     *config.Snapshot
	BuiltinTools             []tools.Tool
	PluginToolsBuilder       PluginToolsBuilder
	ProviderStreamBuilder    ProviderStreamBuilder
	PromptToolsBuilder       prompt.ToolsBuilder
	PromptSectionsBuilder    prompt.SectionsBuilder
	SessionPluginViewBuilder SessionPluginViewBuilder
	ToolLifecycle            *coreagent.ToolLifecycle
	SandboxBackendFn         func(ctx context.Context) string
	VaultEnvLoader           sandbox.VaultEnvLoader
	TokenService             *auth.TokenService
	TokenManager             *oauth.TokenManager
	ProjectResolver          ProjectResolverFunc
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
			if params.UserID != "" {
				userDir, err = SetupUserWorkspace(cfg.Snap.AgentID, config.StellaHome(), params.UserID)
			} else {
				userDir, err = SetupSystemWorkspace(cfg.Snap.AgentID, config.StellaHome())
			}
			if err != nil {
				return nil, fmt.Errorf("setup workspace: %w", err)
			}
			userRoot := userDir

			// Resolve project directory when session has a project.
			var projectRoot string
			if params.ProjectID != "" && cfg.ProjectResolver != nil {
				dir, err := cfg.ProjectResolver(ctx, params.ProjectID, params.UserID)
				if err != nil {
					slog.Warn("project resolution failed", "project_id", params.ProjectID, "error", err)
				} else if dir != "" {
					if err := ValidateProjectDir(dir, userRoot); err != nil {
						slog.Warn("project dir validation failed", "project_id", params.ProjectID, "base_dir", dir, "error", err)
					} else {
						projectRoot = dir
					}
				}
			}

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
				ProjectRoot:         projectRoot,
				UserID:              params.UserID,
				AgentID:             params.AgentID,
				UserRoot:            userRoot,
				RegisteredPluginIDs: append([]string(nil), pluginView.RegisteredPluginIDs...),
				EnabledPluginIDs:    append([]string(nil), pluginView.EnabledPluginIDs...),
			}
			var sections []pkgplugins.SystemPromptSection
			if cfg.PromptSectionsBuilder != nil {
				sections, _ = cfg.PromptSectionsBuilder(ctx, promptBuild)
			}
			if skillsSection, err := skillstool.BuildPromptSection(ctx, promptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
				sections = append(sections, skillsSection)
			}

			// Build the full system prompt per-session with profile from memory provider.
			system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
				SystemPrompt: cfg.Snap.SystemPrompt,
				AgentSoul:    cfg.Snap.Soul,
				Memory:       memProvider,
				UserID:       params.UserID,
				AgentID:      params.AgentID,
				StellaHome:   config.StellaHome(),
				AgentRoot:    cfg.Snap.Workspace,
				UserRoot:     userRoot,
				PromptTools:  promptTools,
				Sections:     sections,
			})

			// Resolve hooks from RunnerParams — injected by Pool, not the factory.
			var hookPlugins []hooks.HookPlugin
			if params.HooksFn != nil {
				hookPlugins = params.HooksFn()
			}

			runnerTools := append([]tools.Tool{}, cfg.BuiltinTools...)
			runnerTools = append(runnerTools, params.ExtraTools...)

			return newRunner(ctx, runnerConfig{
				Provider: providerConfig{
					API:     apiName,
					Model:   modelID,
					APIKey:  creds.APIKey,
					BaseURL: creds.BaseURL,
					Builder: cfg.ProviderStreamBuilder,
				},
				Sandbox: sandbox.Config{
					SandboxConfig:    cfg.Snap.Sandbox,
					SandboxBackendFn: cfg.SandboxBackendFn,
					Paths: sandbox.Paths{
						StellaHome:  config.StellaHome(),
						AgentRoot:   cfg.Snap.Workspace,
						UserRoot:    userRoot,
						ProjectRoot: projectRoot,
					},
					UserID:          params.UserID,
					AgentID:         params.AgentID,
					SessionID:       params.SessionID,
					SessionEnvSpecs: append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
					VaultEnvLoader:  cfg.VaultEnvLoader,
					TokenService:    cfg.TokenService,
					TokenManager:    cfg.TokenManager,
				},
				System:          system,
				Sections:        sections,
				ExtraTools:      runnerTools,
				PluginTools:     cfg.PluginToolsBuilder,
				HookPlugins:     hookPlugins,
				ToolLifecycle:   cfg.ToolLifecycle,
				DelegateTimeout: cfg.Snap.Runner.DelegateTimeoutDuration(),
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", cfg.Snap.Runner.Type)
	}
}
