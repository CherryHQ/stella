package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/skills"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
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

type DomainToolMount struct {
	Name         string
	Tool         tools.Tool
	Predicate    func(RunnerParams) bool
	PredicateCtx func(context.Context, RunnerParams) bool
}

func DomainToolAvailable(params RunnerParams) bool {
	return params.UserID != "" && params.AgentID != ""
}

func domainToolMountEnabled(ctx context.Context, mount DomainToolMount, params RunnerParams) bool {
	if mount.PredicateCtx != nil {
		return mount.PredicateCtx(ctx, params)
	}
	if mount.Predicate != nil {
		return mount.Predicate(params)
	}
	return true
}

// runnerBuilderConfig holds all dependencies needed to assemble a NewRunnerFunc.
type runnerBuilderConfig struct {
	Snap                     *config.Snapshot
	BuiltinTools             []tools.Tool
	PluginToolsBuilder       PluginToolsBuilder
	ProviderStreamBuilder    ProviderStreamBuilder
	PromptSectionsBuilder    prompt.SectionsBuilder
	SessionPluginViewBuilder SessionPluginViewBuilder
	SkillStore               pkgplugins.SkillStore
	MCPToolProvider          MCPToolProvider
	DomainToolMounts         []DomainToolMount
	ToolLifecycle            *coreagent.ToolLifecycle
	SandboxBackendFn         func(ctx context.Context) string
	VaultEnvLoader           sandbox.VaultEnvLoader
	TokenEnsurer             sandbox.TokenEnsurer
	TokenManager             *oauth.TokenManager
	ProjectResolver          ProjectResolverFunc
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
		apiName := creds.Type
		if apiName == "" {
			apiName = provID
		}

		stellaHome := config.StellaHome()
		var (
			userDir string
			// projectValidateRoot is the per-(principal, agent) dir a project must
			// live under: a project is owned by the agent (see #442), so it stays
			// scoped to the agent's subdir of the shared user/group home.
			projectValidateRoot string
			err                 error
		)
		switch {
		case params.GroupID != "":
			userDir, err = SetupGroupWorkspace(stellaHome, params.GroupID, cfg.Snap.AgentID)
			projectValidateRoot = GroupAgentDir(stellaHome, params.GroupID, cfg.Snap.AgentID)
		case params.UserID != "":
			userDir, err = SetupUserWorkspace(stellaHome, params.UserID, cfg.Snap.AgentID)
			projectValidateRoot = UserAgentDir(stellaHome, params.UserID, cfg.Snap.AgentID)
		default:
			// A user-less agent job (e.g. a builtin scheduled job) has no
			// principal home; it runs in the agent's own pool workspace (#442).
			userDir = cfg.Snap.Workspace
			projectValidateRoot = userDir
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
		promptBuild := pkgplugins.SystemPromptContext{
			StellaHome:          config.StellaHome(),
			HomeDir:             homeDir,
			AgentRoot:           cfg.Snap.Workspace,
			ProjectRoot:         projectRoot,
			UserID:              params.UserID,
			AgentID:             params.AgentID,
			UserRoot:            userRoot,
			WorkspaceRoot:       userRoot,
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
			if secrets, err := cfg.VaultEnvLoader.ListDeclarableForAgentProject(ctx, params.UserID, params.AgentID, params.ProjectID); err == nil && len(secrets) > 0 {
				sections = append(sections, pkgplugins.SystemPromptSection{
					Title:   "Declarable Secrets",
					Content: "The following vault secrets can be injected into a single bash command by passing their names in the bash `secrets` parameter. Values are never shown; only declare the names needed for that command.\n\n" + formatDeclarableSecrets(secrets),
				})
			}
		}

		// Build the full system prompt per-session with profile from memory provider.
		// Group sessions skip private profile injection (D9 isolation); group memory
		// is Phase 3 concern.
		promptUserID := params.UserID
		if params.GroupID != "" {
			promptUserID = ""
		}
		system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
			SystemPrompt: cfg.Snap.SystemPrompt,
			AgentSoul:    cfg.Snap.Soul,
			Memory:       memProvider,
			UserID:       promptUserID,
			AgentID:      params.AgentID,
			GroupID:      params.GroupID,
			StellaHome:   config.StellaHome(),
			AgentRoot:    cfg.Snap.Workspace,
			ProjectRoot:  projectRoot,
			UserRoot:     userRoot,
			Sections:     sections,
		})

		// Resolve hooks from RunnerParams — injected by Pool, not the builder.
		var hookPlugins []hooks.HookPlugin
		if params.HooksFn != nil {
			hookPlugins = params.HooksFn()
		}

		sandboxCfg := sandbox.Config{
			SandboxConfig:    cfg.Snap.Sandbox,
			SandboxBackendFn: cfg.SandboxBackendFn,
			Paths: sandbox.Paths{
				StellaHome:  config.StellaHome(),
				AgentRoot:   cfg.Snap.Workspace,
				UserRoot:    userRoot,
				ProjectRoot: projectRoot,
			},
			UserID:          params.UserID,
			GroupID:         params.GroupID,
			AgentID:         params.AgentID,
			SessionID:       params.SessionID,
			ProjectID:       params.ProjectID,
			SessionEnvSpecs: append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
			VaultEnvLoader:  cfg.VaultEnvLoader,
			TokenEnsurer:    cfg.TokenEnsurer,
			TokenManager:    cfg.TokenManager,
		}

		runnerTools := append([]tools.Tool{}, cfg.BuiltinTools...)
		runnerTools = append(runnerTools, params.ExtraTools...)
		workerDispatch := false
		for _, tool := range params.ExtraTools {
			if tool != nil && tool.Definition().Name == "goal_control" {
				workerDispatch = true
				break
			}
		}
		// Worker dispatch sessions get core tools + goal_control only.
		if !workerDispatch {
			for _, mount := range cfg.DomainToolMounts {
				if mount.Tool == nil {
					return nil, fmt.Errorf("domain tool %q is nil", mount.Name)
				}
				if !domainToolMountEnabled(ctx, mount, params) {
					continue
				}
				runnerTools = append(runnerTools, mount.Tool)
			}
		}
		if cfg.SkillStore != nil {
			// User skills live under the shared user-data root (mounted as /user); the
			// skill_dir emitted to the model is remapped to the sandbox-visible path for
			// the active backend so it resolves in bash and never leaks a host path. Use
			// ResolvePaths' canonicalized (symlink-evaluated) roots for BOTH the tool's
			// host paths and the view, so the view's prefix match can't miss on symlinks.
			// On resolve failure the sandbox session will fail downstream anyway; until
			// then, omit every skill_dir (Isolated, no roots) rather than risk emitting a
			// host path the model could leak or fail to resolve.
			stellaHome := config.StellaHome()
			toolProjectRoot := projectRoot
			// Until ResolvePaths succeeds the view has no roots (Isolated drops every
			// skill_dir), so the layout is inert; keep it empty to match that intent
			// rather than emit dirs that would never be remapped.
			layout := skills.SkillDiskLayout{}
			view := skillstool.SkillDirView{Isolated: true}
			if resolved, err := sandbox.ResolvePaths(sandboxCfg); err == nil {
				stellaHome = resolved.StellaHome
				toolProjectRoot = resolved.ProjectRoot
				layout = skillDiskLayout(SystemDBSkillsDir(resolved.StellaHome), resolved.AgentRoot, resolved.UserDataDir, resolved.WorkspaceRoot)
				sv := sandbox.ResolveSkillView(ctx, sandboxCfg, resolved)
				view = skillstool.SkillDirView{
					Isolated:           sv.Isolated,
					SystemSkillsHost:   sv.SystemSkillsHost,
					SystemSkillsView:   sv.SystemSkillsView,
					AgentSkillsHost:    sv.AgentSkillsHost,
					AgentSkillsView:    sv.AgentSkillsView,
					SystemDBSkillsHost: sv.SystemDBSkillsHost,
					SystemDBSkillsView: sv.SystemDBSkillsView,
					UserDataHost:       sv.UserDataHost,
					UserDataView:       sv.UserDataView,
					WorkspaceHost:      sv.WorkspaceHost,
					WorkspaceView:      sv.WorkspaceView,
				}
			}
			runnerTools = append(runnerTools, skillstool.NewTool(
				cfg.SkillStore,
				stellaHome,
				toolProjectRoot,
			).WithSkillDiskLayout(layout).
				WithSkillDirView(view).
				WithPluginVisibility(pluginView.RegisteredPluginIDs, pluginView.EnabledPluginIDs))
		}

		// External MCP-server tools, resolved for this (user, agent) context and
		// namespaced (mcp__<server>__<tool>) so they never collide with core,
		// plugin, or skill tools. A down server is skipped inside the provider.
		if cfg.MCPToolProvider != nil {
			runnerTools = append(runnerTools, cfg.MCPToolProvider.ToolsForContext(ctx, params.UserID, params.AgentID)...)
		}

		return newRunner(ctx, runnerConfig{
			Provider: providerConfig{
				API:     apiName,
				Model:   modelID,
				APIKey:  creds.APIKey,
				BaseURL: creds.BaseURL,
				Builder: cfg.ProviderStreamBuilder,
			},
			Thinking:        params.Thinking,
			Sandbox:         sandboxCfg,
			System:          system,
			Sections:        sections,
			ExtraTools:      runnerTools,
			PluginTools:     cfg.PluginToolsBuilder,
			HookPlugins:     hookPlugins,
			ToolLifecycle:   cfg.ToolLifecycle,
			DelegateRunner:  params.DelegateRunner,
			DelegateTimeout: cfg.Snap.Runner.DelegateTimeoutDuration(),
		})
	}
}
