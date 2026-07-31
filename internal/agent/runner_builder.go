package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/vault"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// MCPToolProvider surfaces external MCP-server tools into an agent's tool
// registry for a (user, agent) context. Implemented by *mcp.ToolProvider; kept
// as an interface here so the agent package need not depend on the MCP client
// internals and tests can stub it.
type MCPToolProvider interface {
	ToolsForContext(ctx context.Context, userID, agentID string) []tools.Tool
}

type BuiltinTool struct {
	Tool      tools.Tool
	Available func(context.Context, RunnerParams) bool
}

func BuiltinToolAvailable(_ context.Context, params RunnerParams) bool {
	return params.UserID != "" && params.AgentID != ""
}

func NonGroupBuiltinToolAvailable(ctx context.Context, params RunnerParams) bool {
	return params.GroupID == "" && BuiltinToolAvailable(ctx, params)
}

func GroupBuiltinToolAvailable(_ context.Context, params RunnerParams) bool {
	return params.GroupID != "" && params.AgentID != ""
}

// runnerBuilderConfig holds all dependencies needed to assemble a NewRunnerFunc.
type runnerBuilderConfig struct {
	Snap                     *config.Snapshot
	BuiltinTools             []BuiltinTool
	PluginToolsBuilder       PluginToolsBuilder
	ProviderStreamBuilder    ProviderStreamBuilder
	PromptSectionsBuilder    prompt.SectionsBuilder
	SessionPluginViewBuilder SessionPluginViewBuilder
	SkillStore               pkgplugins.SkillStore
	SkillReadAuthorizer      skillstool.SkillReadAuthorizer
	MCPToolProvider          MCPToolProvider
	ToolOverrideFetcher      ToolOverrideFetcher
	ToolLifecycle            *coreagent.ToolLifecycle
	SandboxBackendFn         func(ctx context.Context) string
	VaultEnvLoader           sandbox.VaultEnvLoader
	TokenManager             *oauth.TokenManager
	ProjectResolver          ProjectResolverFunc
	StructuredGroupMemory    bool
}

// newRunnerFunc assembles a NewRunnerFunc for a given config snapshot.
// The returned func creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. Runner execution is always user-scoped,
// so per-user workspace directories are created for every runner instance.
//
// Hooks are not part of the builder — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
func newRunnerFunc(cfg runnerBuilderConfig) NewRunnerFunc {
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
		if params.GroupID != "" && cfg.StructuredGroupMemory {
			groupModel := cfg.Snap.ResolveModelRef(modelRef)
			if groupModel.ContextWindow < config.GroupMemoryMinimumContextWindow {
				return nil, fmt.Errorf(
					"group chat model %q declares context window %d; structured group memory requires at least %d",
					modelRef,
					groupModel.ContextWindow,
					config.GroupMemoryMinimumContextWindow,
				)
			}
		}
		apiName := creds.Type
		if apiName == "" {
			apiName = provID
		}

		stellaHome := config.StellaHome()
		var (
			userRoot string
			// projectValidateRoot is the per-(principal, agent) dir a project must
			// live under: a project is owned by the agent (see #442), so it stays
			// scoped to the agent's subdir of the shared user/group home.
			projectValidateRoot string
		)
		if params.UserID != "" || params.GroupID != "" {
			principal, err := SetupPrincipalWorkspace(stellaHome, params.UserID, params.GroupID, cfg.Snap.AgentID)
			if err != nil {
				return nil, fmt.Errorf("setup workspace: %w", err)
			}
			userRoot = principal.HomeDir
			projectValidateRoot = principal.AgentDir
		} else {
			// A user-less agent job (e.g. a builtin scheduled job) has no
			// principal home; it runs in the agent's own pool workspace (#442).
			userRoot = cfg.Snap.Workspace
			projectValidateRoot = userRoot
		}

		// Resolve project directory when session has a project.
		var projectRoot string
		if params.ProjectID != "" && cfg.ProjectResolver != nil {
			dir, err := cfg.ProjectResolver(ctx, params.ProjectID, params.UserID)
			if err != nil {
				slog.Warn("project resolution failed", "project_id", params.ProjectID, "error", err)
			} else if dir != "" {
				if err := ValidateProjectDir(dir, projectValidateRoot); err != nil {
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

		homeDir, _ := os.UserHomeDir()
		pluginView := pkgplugins.SessionPluginView{}
		if cfg.SessionPluginViewBuilder != nil {
			pluginView, _ = cfg.SessionPluginViewBuilder(ctx)
		}
		promptUserID := params.UserID
		if params.GroupID != "" {
			// A group session has a synthetic UserID equal to group_id. It is
			// workspace identity, not a Skill owner.
			promptUserID = ""
		}
		promptBuild := pkgplugins.SystemPromptContext{
			StellaHome:          config.StellaHome(),
			HomeDir:             homeDir,
			AgentRoot:           cfg.Snap.Workspace,
			ProjectRoot:         projectRoot,
			UserID:              promptUserID,
			AgentID:             params.AgentID,
			UserRoot:            userRoot,
			WorkspaceRoot:       projectValidateRoot,
			SkillStore:          cfg.SkillStore,
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
		if params.GroupID == "" && cfg.VaultEnvLoader != nil {
			metas, err := cfg.VaultEnvLoader.ListAmbientSecretMetas(ctx, params.UserID, params.AgentID)
			if err != nil {
				slog.Warn("vault secret metadata unavailable",
					"component", "runner_builder",
					"user_id", params.UserID,
					"agent_id", params.AgentID,
					"project_id", params.ProjectID,
					"error", err,
				)
			} else if len(metas) > 0 {
				sections = append(sections, pkgplugins.SystemPromptSection{
					Title:   "Available Secrets",
					Content: "These vault secret names are already available as environment variables in bash. Values are never shown; use the names exactly as the CLI or tool expects.\n\n" + formatAvailableSecretMetas(metas),
				})
			}
		}

		// Build the full system prompt per session. Group sessions skip private
		// profile injection; structured Group Facts are added per turn after
		// plugin prompt hooks have completed.
		system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
			SystemPrompt:          cfg.Snap.SystemPrompt,
			AgentSoul:             cfg.Snap.Soul,
			Memory:                memProvider,
			UserID:                promptUserID,
			AgentID:               params.AgentID,
			GroupID:               params.GroupID,
			StellaHome:            config.StellaHome(),
			AgentRoot:             cfg.Snap.Workspace,
			ProjectRoot:           projectRoot,
			UserRoot:              userRoot,
			Sections:              sections,
			StructuredGroupMemory: cfg.StructuredGroupMemory,
		})

		// Resolve hooks from RunnerParams — injected by Pool, not the builder.
		var hookPlugins []hooks.HookPlugin
		if params.HooksFn != nil {
			hookPlugins = params.HooksFn()
		}

		sessionSecretValues := sandbox.NewSessionSecretValues()
		sandboxCfg := sandbox.Config{
			SandboxConfig:    cfg.Snap.Sandbox,
			SandboxBackendFn: cfg.SandboxBackendFn,
			Paths: sandbox.Paths{
				StellaHome:  config.StellaHome(),
				AgentRoot:   cfg.Snap.Workspace,
				UserRoot:    userRoot,
				ProjectRoot: projectRoot,
			},
			UserID:              params.UserID,
			GroupID:             params.GroupID,
			AgentID:             params.AgentID,
			SessionID:           params.SessionID,
			ProjectID:           params.ProjectID,
			SessionEnvSpecs:     append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
			VaultEnvLoader:      cfg.VaultEnvLoader,
			SessionSecretValues: sessionSecretValues,
			TokenManager:        cfg.TokenManager,
			OAuthEnvBindings:    sandbox.NewOAuthEnvBindings(),
		}

		builtinTools := append([]BuiltinTool(nil), cfg.BuiltinTools...)
		perRunTools := append([]tools.Tool(nil), params.ExtraTools...)

		return newRunner(ctx, runnerConfig{
			Provider: providerConfig{
				API:     apiName,
				Model:   modelID,
				APIKey:  creds.APIKey,
				BaseURL: creds.BaseURL,
				Builder: cfg.ProviderStreamBuilder,
			},
			Thinking:            params.Thinking,
			Sandbox:             sandboxCfg,
			System:              system,
			Sections:            sections,
			BuiltinTools:        builtinTools,
			BuiltinParams:       params,
			PerRunTools:         perRunTools,
			SkillStore:          cfg.SkillStore,
			SkillReadAuthorizer: cfg.SkillReadAuthorizer,
			PluginView:          pluginView,
			MCPToolProvider:     cfg.MCPToolProvider,
			ToolOverrideFetcher: cfg.ToolOverrideFetcher,
			PluginTools:         cfg.PluginToolsBuilder,
			HookPlugins:         hookPlugins,
			ToolLifecycle:       cfg.ToolLifecycle,
			DelegateRunner:      params.DelegateRunner,
			DelegateTimeout:     cfg.Snap.Runner.DelegateTimeoutDuration(),
		})
	}
}

func formatAvailableSecretMetas(metas []vault.AmbientSecretMeta) string {
	lines := make([]string, 0, len(metas))
	for _, meta := range metas {
		line := meta.Name
		if meta.Description != "" {
			line += " — " + meta.Description
		}
		lines = append(lines, line)
	}
	return "- " + strings.Join(lines, "\n- ")
}
