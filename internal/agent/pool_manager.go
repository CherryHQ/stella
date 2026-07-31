package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	pluginhooks "github.com/CherryHQ/stella/plugins/hooks"
)

// PluginToolsBuilder creates tools from enabled plugin state.
type PluginToolsBuilder func(ctx context.Context, build pkgplugins.ToolBuildContext) []tools.Tool

// PluginHooksBuilder creates hook plugins from enabled plugin state.
type PluginHooksBuilder func(ctx context.Context) []hooks.HookPlugin

type (
	SessionPluginViewBuilder func(ctx context.Context) (pkgplugins.SessionPluginView, error)
	BeforeRunBuilder         func(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error)
	ProviderStreamBuilder    func(api, apiKey, baseURL string) (providers.StreamFunc, error)
)

// PoolManagerOption configures a PoolManager.
type PoolManagerOption func(*PoolManager)

func WithIdleTimeoutPM(d time.Duration) PoolManagerOption {
	return func(pm *PoolManager) { pm.idleTimeout = d }
}

func WithCompactionPM(cfg CompactionConfig) PoolManagerOption {
	return func(pm *PoolManager) { pm.compaction = cfg }
}

func WithBuiltinTools(tools []BuiltinTool) PoolManagerOption {
	return func(pm *PoolManager) { pm.builtinTools = tools }
}

func WithPluginToolsBuilder(b PluginToolsBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.pluginToolsBuilder = b }
}

func WithPluginHooksBuilder(b PluginHooksBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.pluginHooksBuilder = b }
}

// WithCoreHooks registers server-level hooks (e.g. the OTel trace hook) that
// live for the whole PoolManager lifetime. Unlike plugin hooks they are never
// rebuilt or closed on reload, so in-flight runners can keep calling them; they
// are closed exactly once in Close.
func WithCoreHooks(h []hooks.HookPlugin) PoolManagerOption {
	return func(pm *PoolManager) { pm.coreHooks = h }
}

func WithPromptSectionsBuilder(b prompt.SectionsBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.promptSectionsBuilder = b }
}

func WithSessionPluginViewBuilder(b SessionPluginViewBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.sessionPluginViewBuilder = b }
}

func WithBeforeRunBuilderPM(b BeforeRunBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.beforeRunBuilder = b }
}

func WithToolLifecyclePM(tl *coreagent.ToolLifecycle) PoolManagerOption {
	return func(pm *PoolManager) { pm.toolLifecycle = tl }
}

func WithProviderStreamBuilder(b ProviderStreamBuilder) PoolManagerOption {
	return func(pm *PoolManager) { pm.providerStreamBuilder = b }
}

func WithSkillStore(s pkgplugins.SkillStore) PoolManagerOption {
	return func(pm *PoolManager) { pm.skillStore = s }
}

// WithSkillReadAuthorizer injects Skill domain read access into every runner's
// skills tool, so DB-backed reads (load/search_installed) are authorized.
func WithSkillReadAuthorizer(a skillstool.SkillReadAuthorizer) PoolManagerOption {
	return func(pm *PoolManager) { pm.skillReadAuthz = a }
}

func WithVaultEnvLoader(v sandbox.VaultEnvLoader) PoolManagerOption {
	return func(pm *PoolManager) { pm.vaultEnvLoader = v }
}

// WithMCPToolProvider wires the provider that surfaces external MCP-server tools
// into each agent's tool registry. Optional: nil means no MCP tools.
func WithMCPToolProvider(p MCPToolProvider) PoolManagerOption {
	return func(pm *PoolManager) { pm.mcpToolProvider = p }
}

func WithToolOverrideFetcher(f ToolOverrideFetcher) PoolManagerOption {
	return func(pm *PoolManager) { pm.toolOverrideFetcher = f }
}

func WithTokenManager(tm *oauth.TokenManager) PoolManagerOption {
	return func(pm *PoolManager) { pm.tokenManager = tm }
}

func WithProjectResolver(r ProjectResolverFunc) PoolManagerOption {
	return func(pm *PoolManager) { pm.projectResolver = r }
}

func WithProjectEnsurerPM(fn ProjectEnsurerFunc) PoolManagerOption {
	return func(pm *PoolManager) { pm.projectEnsurer = fn }
}

// WithAssetStorePM injects the authoritative asset store used for cold-pod asset
// hydration. When unset (e.g. in tests), hydration is skipped.
func WithAssetStorePM(a *asset.Store) PoolManagerOption {
	return func(pm *PoolManager) { pm.assets = a }
}

// PoolManager manages one Service per enabled agent. It reads enabled agents
// from the config Store and creates a Service (session.Registry + runtime.Runtime)
// per agent.
type PoolManager struct {
	services map[string]*Service
	store    config.Store
	mem      memory.Provider
	mu       sync.RWMutex
	// started is set true when StartAll runs. The one-shot pre-start binds
	// (Bind* below) refuse to run once started, while the dynamic reconfigure
	// surface (ReloadPlugin*/SyncAgent/Invalidate*) stays available afterward.
	started                  bool
	idleTimeout              time.Duration
	compaction               CompactionConfig
	builtinTools             []BuiltinTool
	pluginToolsBuilder       PluginToolsBuilder
	hookPlugins              []hooks.HookPlugin
	coreHooks                []hooks.HookPlugin
	pluginHooksBuilder       PluginHooksBuilder
	promptSectionsBuilder    prompt.SectionsBuilder
	sessionPluginViewBuilder SessionPluginViewBuilder
	beforeRunBuilder         BeforeRunBuilder
	toolLifecycle            *coreagent.ToolLifecycle
	providerStreamBuilder    ProviderStreamBuilder
	skillStore               pkgplugins.SkillStore
	skillReadAuthz           skillstool.SkillReadAuthorizer
	mcpToolProvider          MCPToolProvider
	toolOverrideFetcher      ToolOverrideFetcher
	vaultEnvLoader           sandbox.VaultEnvLoader
	projectResolver          ProjectResolverFunc
	projectEnsurer           ProjectEnsurerFunc
	tokenManager             *oauth.TokenManager
	oauthRegistry            *oauth.ProviderRegistry
	assets                   *asset.Store
	sessionAccess            SessionAccessService
	log                      *slog.Logger
}

func NewPoolManager(store config.Store, mem memory.Provider, opts ...PoolManagerOption) *PoolManager {
	pm := &PoolManager{
		services:    make(map[string]*Service),
		store:       store,
		mem:         mem,
		idleTimeout: 10 * time.Minute,
		log:         slog.With("component", "pool_manager"),
	}
	for _, opt := range opts {
		opt(pm)
	}
	return pm
}

// BindOAuthRegistry binds the OAuth provider registry before StartAll. It is a
// one-shot pre-start bind: it rejects a nil registry (missing), a second bind
// (duplicate), and any bind after StartAll (late).
func (pm *PoolManager) BindOAuthRegistry(r *oauth.ProviderRegistry) error {
	if r == nil {
		return errors.New("agent: BindOAuthRegistry requires a non-nil registry")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.started {
		return errors.New("agent: BindOAuthRegistry after StartAll")
	}
	if pm.oauthRegistry != nil {
		return errors.New("agent: OAuth registry already bound")
	}
	pm.oauthRegistry = r
	if pm.tokenManager != nil {
		pm.tokenManager.SetRegistry(r)
	}
	return nil
}

// BindVaultEnvLoader binds the sandbox vault env loader (and the derived OAuth
// token manager) before StartAll. One-shot pre-start bind: rejects nil
// (missing), a second bind (duplicate), and any bind after StartAll (late).
// Because it runs before agents start, no runner rebuild is needed.
func (pm *PoolManager) BindVaultEnvLoader(v sandbox.VaultEnvLoader) error {
	if v == nil {
		return errors.New("agent: BindVaultEnvLoader requires a non-nil loader")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.started {
		return errors.New("agent: BindVaultEnvLoader after StartAll")
	}
	if pm.vaultEnvLoader != nil {
		return errors.New("agent: vault env loader already bound")
	}
	pm.vaultEnvLoader = v
	if vs, ok := v.(oauth.VaultStore); ok {
		pm.tokenManager = oauth.NewTokenManager(vs)
		if pm.oauthRegistry != nil {
			pm.tokenManager.SetRegistry(pm.oauthRegistry)
		}
	}
	return nil
}

// BindSessionAccess binds the shared Session PEP before StartAll. All non-HTTP
// entry session lifecycle in Service goes through this port.
func (pm *PoolManager) BindSessionAccess(access SessionAccessService) error {
	if access == nil {
		return errors.New("agent: BindSessionAccess requires a non-nil service")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.started {
		return errors.New("agent: BindSessionAccess after StartAll")
	}
	if pm.sessionAccess != nil {
		return errors.New("agent: session access already bound")
	}
	pm.sessionAccess = access
	return nil
}

// BindMCPToolProvider binds the MCP tool provider before StartAll. One-shot
// pre-start bind: rejects nil (missing), a second bind (duplicate), and any bind
// after StartAll (late). No runner rebuild is needed because agents have not yet
// started.
func (pm *PoolManager) BindMCPToolProvider(p MCPToolProvider) error {
	if p == nil {
		return errors.New("agent: BindMCPToolProvider requires a non-nil provider")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.started {
		return errors.New("agent: BindMCPToolProvider after StartAll")
	}
	if pm.mcpToolProvider != nil {
		return errors.New("agent: MCP tool provider already bound")
	}
	pm.mcpToolProvider = p
	return nil
}

// HookPlugins returns a snapshot of the active hook plugins: the reloadable
// user plugins plus the stable core hooks. Ordering is irrelevant — NewHookSet
// sorts by Priority — so core hooks are simply appended.
func (pm *PoolManager) HookPlugins() []hooks.HookPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make([]hooks.HookPlugin, 0, len(pm.hookPlugins)+len(pm.coreHooks))
	out = append(out, pm.hookPlugins...)
	out = append(out, pm.coreHooks...)
	return out
}

// StartAll reads enabled agents from the store and creates a Service per agent.
func (pm *PoolManager) StartAll(ctx context.Context) error {
	// Seal the pre-start binds: after this point the static Vault/MCP/OAuth
	// capabilities and builtin tools are fixed. StartAll is one-shot.
	pm.mu.Lock()
	if pm.started {
		pm.mu.Unlock()
		return errors.New("agent: PoolManager.StartAll called more than once")
	}
	if pm.sessionAccess == nil {
		pm.mu.Unlock()
		return errors.New("agent: PoolManager.StartAll requires SessionAccess")
	}
	pm.started = true
	pm.mu.Unlock()

	if pm.pluginHooksBuilder != nil {
		pm.hookPlugins = pm.pluginHooksBuilder(ctx)
	}

	agents, err := pm.store.ListEnabledAgents(ctx)
	if err != nil {
		pm.log.Warn("could not list agents at startup", "error", err)
		return nil
	}
	if len(agents) == 0 {
		pm.log.Info("no enabled agents found, pool manager started empty")
		return nil
	}

	for _, ag := range agents {
		if err := pm.startAgent(ctx, ag); err != nil {
			pm.log.Error("failed to start agent", "agent_id", ag.ID, "error", err)
			continue
		}
	}

	pm.mu.RLock()
	count := len(pm.services)
	pm.mu.RUnlock()

	if count == 0 {
		pm.log.Warn("agents found but none could be started")
		return nil
	}

	pm.log.Info("all agents started", "count", count)
	return nil
}

func (pm *PoolManager) startAgent(ctx context.Context, ag config.Agent) error {
	snap, workspace, err := pm.loadAgentSnapshot(ctx, ag.ID)
	if err != nil {
		return err
	}

	factory := pm.buildRunnerFunc(ctx, snap)

	svc, err := pm.buildService(ctx, ag.ID, factory, snap)
	if err != nil {
		return fmt.Errorf("build service for agent %q: %w", ag.ID, err)
	}

	pm.mu.Lock()
	pm.services[ag.ID] = svc
	pm.mu.Unlock()

	pm.log.Info("agent started", "agent_id", ag.ID, "workspace", workspace)
	return nil
}

func (pm *PoolManager) buildService(ctx context.Context, agentID string, factory NewRunnerFunc, snap *config.Snapshot) (*Service, error) {
	reg, err := session.NewRegistry(pm.mem, agentID)
	if err != nil {
		return nil, fmt.Errorf("build session registry for %q: %w", agentID, err)
	}

	cfg := agentruntime.Config{
		NewRunner:       factory,
		Memory:          pm.mem,
		IdleTimeout:     pm.idleTimeout,
		DefaultModel:    snap.ResolveModelID(config.ModelTierStrong),
		DefaultThinking: snap.ResolveThinkingLevel(config.ModelTierStrong),
		HooksFn:         pm.HookPlugins,
		BeforeRun:       pm.runtimeBeforeRunFunc(snap),
		SnapshotPrompt:  pm.buildSnapshotPromptFunc(snap),
		Compaction: agentruntime.CompactionConfig{
			MaxTokens: pm.compaction.WithDefaults().MaxTokens,
			KeepTail:  pm.compaction.WithDefaults().KeepTail,
		},
	}
	rt, err := agentruntime.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("build runtime for %q: %w", agentID, err)
	}
	go rt.StartReaper(ctx)

	pm.mu.RLock()
	sessionAccess := pm.sessionAccess
	pm.mu.RUnlock()
	if sessionAccess == nil {
		return nil, errors.New("session access is not bound")
	}
	svc := &Service{Sessions: reg, Runtime: rt, SessionAccess: sessionAccess, AgentID: agentID}
	rt.SetDelegateRunner(svc)
	return svc, nil
}

// promptScope computes the per-session workspace root and the prompt subject for
// profile rendering. Group sessions blank the prompt UserID so the group id is
// never treated as a human profile subject (D9); plugin/skill sections still use
// the session's UserID (the group id) so they match the cached group runner.
func (pm *PoolManager) promptScope(agentID string, info session.Info) (userRoot, promptUserID, groupID string) {
	if info.GroupID != "" || info.UserID != "" {
		if workspace, err := SetupPrincipalWorkspace(config.StellaHome(), info.UserID, info.GroupID, agentID); err == nil {
			userRoot = workspace.HomeDir
			pm.hydrateAssets(workspace.HomeDir)
		}
	}
	if info.GroupID != "" {
		return userRoot, "", info.GroupID
	}
	return userRoot, info.UserID, ""
}

// hydrateAssets restores the principal's assets subtree from the shared asset
// authority in the background, so a cold pod's empty assets tree fills in shortly
// after the session starts without adding to startup latency. Single-flight per
// tree lives in asset.Store.HydrateUser. context.Background() (not a request ctx)
// keeps the copy from being cancelled when the caller returns. No-op when no asset
// store is configured (e.g. tests) or the deployment has no shared authority.
func (pm *PoolManager) hydrateAssets(home string) {
	if pm.assets == nil {
		return
	}
	assets := pm.assets
	go func() {
		if err := assets.HydrateUser(context.Background(), UserAssetsDir(home)); err != nil {
			pm.log.Warn("hydrate user assets failed", "home", home, "error", err)
		}
	}()
}

func (pm *PoolManager) promptSections(ctx context.Context, snap *config.Snapshot, info session.Info, userRoot string) []pkgplugins.SystemPromptSection {
	homeDir, _ := os.UserHomeDir()
	pluginView := pkgplugins.SessionPluginView{}
	if pm.sessionPluginViewBuilder != nil {
		pluginView, _ = pm.sessionPluginViewBuilder(ctx)
	}
	workspaceRoot := snap.Workspace
	if userRoot != "" {
		workspaceRoot = AgentDirInHome(userRoot, info.AgentID)
	}
	promptBuild := pkgplugins.SystemPromptContext{
		StellaHome:          config.StellaHome(),
		HomeDir:             homeDir,
		AgentRoot:           snap.Workspace,
		UserID:              info.UserID,
		AgentID:             info.AgentID,
		UserRoot:            userRoot,
		WorkspaceRoot:       workspaceRoot,
		SkillStore:          pm.skillStore,
		RegisteredPluginIDs: append([]string(nil), pluginView.RegisteredPluginIDs...),
		EnabledPluginIDs:    append([]string(nil), pluginView.EnabledPluginIDs...),
	}
	var sections []pkgplugins.SystemPromptSection
	if pm.promptSectionsBuilder != nil {
		sections, _ = pm.promptSectionsBuilder(ctx, promptBuild)
	}
	if skillsSection, err := skillstool.BuildPromptSection(ctx, promptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
		sections = append(sections, skillsSection)
	}
	return sections
}

func (pm *PoolManager) buildSnapshotPromptFunc(snap *config.Snapshot) agentruntime.SnapshotPromptFunc {
	return func(ctx context.Context, info session.Info, ss memory.SessionSnapshot, privateHuman bool) string {
		// Keep an addressable copy so version zero remains an explicit snapshot.
		version := ss.Version
		userRoot, promptUserID, groupID := pm.promptScope(snap.AgentID, info)
		sections := pm.promptSections(ctx, snap, info, userRoot)

		return prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
			SystemPrompt:    snap.SystemPrompt,
			AgentSoul:       snap.Soul,
			Memory:          pm.mem,
			UserID:          promptUserID,
			AgentID:         info.AgentID,
			GroupID:         groupID,
			StellaHome:      config.StellaHome(),
			AgentRoot:       snap.Workspace,
			UserRoot:        userRoot,
			Sections:        sections,
			SnapshotVersion: &version,
			KnowledgeAvailable: KnowledgeToolAvailable(ctx, RunnerParams{
				UserID:         info.UserID,
				GroupID:        info.GroupID,
				AgentID:        info.AgentID,
				SessionKind:    info.Kind,
				SessionChannel: info.Channel,
				PrivateHuman:   privateHuman,
			}),
		})
	}
}

func (pm *PoolManager) runtimeBeforeRunFunc(snap *config.Snapshot) agentruntime.BeforeRunFunc {
	return func(ctx context.Context, info session.Info, model, msgText, system string, history []ai.Message) (string, error) {
		if pm.beforeRunBuilder == nil {
			return system, nil
		}
		result, err := pm.beforeRunBuilder(ctx, pkgplugins.BeforeRunContext{
			SessionID:    info.ID,
			Channel:      info.Channel,
			UserID:       info.UserID,
			AgentID:      info.AgentID,
			Model:        model,
			MessageText:  msgText,
			SystemPrompt: system,
			History:      append([]ai.Message(nil), history...),
		})
		if err != nil {
			return "", err
		}
		if result.SystemPrompt == "" {
			return system, nil
		}
		return result.SystemPrompt, nil
	}
}

// GetService returns the Service for the given agent ID, or nil if not found.
func (pm *PoolManager) GetService(agentID string) *Service {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.services[agentID]
}

// Default returns any service (first found).
func (pm *PoolManager) Default() *Service {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, svc := range pm.services {
		return svc
	}
	return nil
}

// ReloadPluginTools rebuilds the runner factory for every service.
func (pm *PoolManager) ReloadPluginTools(ctx context.Context) error {
	if pm.pluginToolsBuilder == nil {
		return nil
	}

	pm.mu.RLock()
	agentIDs := make([]string, 0, len(pm.services))
	for id := range pm.services {
		agentIDs = append(agentIDs, id)
	}
	pm.mu.RUnlock()

	for _, agentID := range agentIDs {
		if err := pm.rebuildRunnerFunc(ctx, agentID); err != nil {
			pm.log.Error("failed to rebuild factory after plugin reload", "agent_id", agentID, "error", err)
		}
	}

	pm.log.Info("plugin tools reloaded")
	return nil
}

// ReloadPluginHooks rebuilds the hook plugin set and propagates to every service.
func (pm *PoolManager) ReloadPluginHooks(ctx context.Context) error {
	if pm.pluginHooksBuilder == nil {
		return nil
	}

	hookPlugins := pm.pluginHooksBuilder(ctx)

	pm.mu.Lock()
	oldPlugins := pm.hookPlugins
	pm.hookPlugins = hookPlugins
	services := make(map[string]*Service, len(pm.services))
	maps.Copy(services, pm.services)
	pm.mu.Unlock()

	for _, svc := range services {
		svc.Runtime.SetHooks(pm.HookPlugins)
	}

	pluginhooks.CloseHookPlugins(oldPlugins)
	pm.log.Info("plugin hooks reloaded", "hook_count", len(hookPlugins))
	return nil
}

// ReloadPluginProviders rebuilds the runner factory for every service.
func (pm *PoolManager) ReloadPluginProviders(ctx context.Context) error {
	pm.mu.RLock()
	agentIDs := make([]string, 0, len(pm.services))
	for id := range pm.services {
		agentIDs = append(agentIDs, id)
	}
	pm.mu.RUnlock()

	for _, agentID := range agentIDs {
		if err := pm.rebuildRunnerFunc(ctx, agentID); err != nil {
			pm.log.Error("failed to rebuild factory after provider reload", "agent_id", agentID, "error", err)
		}
	}

	pm.log.Info("plugin providers reloaded")
	return nil
}

// rebuildRunnerFunc rebuilds and replaces the runner builder for a single agent's service.
func (pm *PoolManager) rebuildRunnerFunc(ctx context.Context, agentID string) error {
	snap, _, err := pm.loadAgentSnapshot(ctx, agentID)
	if err != nil {
		return err
	}
	factory := pm.buildRunnerFunc(ctx, snap)

	pm.mu.RLock()
	svc := pm.services[agentID]
	pm.mu.RUnlock()
	if svc != nil {
		svc.Runtime.SetNewRunner(factory)
		svc.Runtime.SetDefaultModel(snap.ResolveModelID(config.ModelTierStrong), snap.ResolveThinkingLevel(config.ModelTierStrong))
		svc.Runtime.SetHooks(pm.HookPlugins)
	}
	return nil
}

// SyncAgent reloads one agent's configuration. If the agent was deleted or
// disabled, its service is closed and removed. Otherwise the factory and
// runners are rebuilt.
func (pm *PoolManager) SyncAgent(ctx context.Context, agentID string) error {
	ag, err := pm.store.GetAgent(ctx, agentID)
	if err != nil {
		return pm.removeAgent(agentID)
	}
	if !ag.Enabled {
		return pm.removeAgent(agentID)
	}

	pm.mu.RLock()
	svc := pm.services[agentID]
	pm.mu.RUnlock()

	if svc == nil {
		return pm.startAgent(ctx, ag)
	}
	if err := pm.rebuildRunnerFunc(ctx, agentID); err != nil {
		return err
	}
	if err := svc.Runtime.ResetRunners(); err != nil {
		pm.log.Warn("failed to reset runners after sync", "agent_id", agentID, "error", err)
	}
	pm.log.Info("agent reloaded", "agent_id", agentID)
	return nil
}

func (pm *PoolManager) removeAgent(agentID string) error {
	pm.mu.Lock()
	svc := pm.services[agentID]
	delete(pm.services, agentID)
	pm.mu.Unlock()
	if svc == nil {
		return nil
	}
	pm.log.Info("removing agent service", "agent_id", agentID)
	return svc.Runtime.Close()
}

func (pm *PoolManager) loadAgentSnapshot(ctx context.Context, agentID string) (*config.Snapshot, string, error) {
	workspace, err := SetupAgentWorkspace(config.StellaHome(), agentID)
	if err != nil {
		return nil, "", fmt.Errorf("setup workspace for agent %q: %w", agentID, err)
	}
	snap, err := pm.store.Snapshot(ctx, agentID)
	if err != nil {
		return nil, "", fmt.Errorf("load snapshot for agent %q: %w", agentID, err)
	}
	snap.Workspace = workspace
	return snap, workspace, nil
}

// buildRunnerFunc assembles a NewRunnerFunc with builtin tools and external plugin tools.
func (pm *PoolManager) buildRunnerFunc(_ context.Context, snap *config.Snapshot) NewRunnerFunc {
	pm.mu.RLock()
	builtinTools := append([]BuiltinTool{}, pm.builtinTools...)
	pm.mu.RUnlock()

	sandboxBackendFn := func(ctx context.Context) string {
		plugins, _ := pm.store.ListPlugins(ctx)
		return config.ActiveSandboxBackend(plugins)
	}
	return newRunnerFunc(runnerBuilderConfig{
		Snap:                     snap,
		BuiltinTools:             builtinTools,
		PluginToolsBuilder:       pm.pluginToolsBuilder,
		ProviderStreamBuilder:    pm.providerStreamBuilder,
		PromptSectionsBuilder:    pm.promptSectionsBuilder,
		SessionPluginViewBuilder: pm.sessionPluginViewBuilder,
		SkillStore:               pm.skillStore,
		SkillReadAuthorizer:      pm.skillReadAuthz,
		MCPToolProvider:          pm.mcpToolProvider,
		ToolOverrideFetcher:      pm.toolOverrideFetcher,
		ToolLifecycle:            pm.toolLifecycle,
		SandboxBackendFn:         sandboxBackendFn,
		VaultEnvLoader:           pm.vaultEnvLoader,
		TokenManager:             pm.tokenManager,
		ProjectResolver:          pm.projectResolver,
	})
}

// AddBuiltinTool appends a builtin tool before StartAll. It is a one-shot
// pre-start bind: it rejects a nil tool, a duplicate tool name, and any add
// after StartAll (post-start rejection), since the builtin-tool set is sealed
// once agents start. Runtime tool changes go through the plugin-tool path
// (pluginToolsBuilder / ReloadPluginTools), not here.
func (pm *PoolManager) AddBuiltinTool(_ context.Context, tool tools.Tool) error {
	if tool == nil {
		return errors.New("agent: AddBuiltinTool requires a non-nil tool")
	}
	name := tool.Definition().Name
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.started {
		return fmt.Errorf("agent: AddBuiltinTool(%q) after StartAll", name)
	}
	for _, bt := range pm.builtinTools {
		if bt.Tool != nil && bt.Tool.Definition().Name == name {
			return fmt.Errorf("agent: builtin tool %q already registered", name)
		}
	}
	pm.builtinTools = append(pm.builtinTools, BuiltinTool{Tool: tool})
	return nil
}

// InvalidateUser closes all live runners for userID across all services.
func (pm *PoolManager) InvalidateUser(userID string) error {
	pm.mu.RLock()
	services := make(map[string]*Service, len(pm.services))
	maps.Copy(services, pm.services)
	pm.mu.RUnlock()

	var lastErr error
	for _, svc := range services {
		if err := svc.Runtime.ResetRunnersForUser(userID); err != nil {
			pm.log.Error("reset runners for user", "user_id", userID, "error", err)
			lastErr = err
		}
	}
	return lastErr
}

// InvalidateUserAgent closes live runners for one user on one agent.
func (pm *PoolManager) InvalidateUserAgent(userID, agentID string) error {
	pm.mu.RLock()
	svc, ok := pm.services[agentID]
	pm.mu.RUnlock()
	if !ok {
		return nil
	}
	if err := svc.Runtime.ResetRunnersForUser(userID); err != nil {
		pm.log.Error("reset runners for user agent", "user_id", userID, "agent_id", agentID, "error", err)
		return err
	}
	return nil
}

// InvalidateAgent closes all live runners for one agent across every user.
func (pm *PoolManager) InvalidateAgent(agentID string) error {
	pm.mu.RLock()
	svc, ok := pm.services[agentID]
	pm.mu.RUnlock()
	if !ok {
		return nil
	}
	if err := svc.Runtime.ResetRunners(); err != nil {
		pm.log.Error("reset runners for agent", "agent_id", agentID, "error", err)
		return err
	}
	return nil
}

// InvalidateAll closes every live runner across all services.
func (pm *PoolManager) InvalidateAll() error {
	pm.mu.RLock()
	services := make(map[string]*Service, len(pm.services))
	maps.Copy(services, pm.services)
	pm.mu.RUnlock()

	var lastErr error
	for id, svc := range services {
		if err := svc.Runtime.ResetRunners(); err != nil {
			pm.log.Error("reset runners for service", "agent_id", id, "error", err)
			lastErr = err
		}
	}
	return lastErr
}

// WaitInFlight blocks until no runtime has an in-flight chat turn or ctx
// expires, returning ctx's error on expiry. Graceful shutdown calls it after
// ingress has stopped and HTTP has drained, so accepted turns that hold no
// HTTP connection (channel messages, webhook runs, scheduler run-now) finish
// before the work contexts are cancelled (#744). It snapshots the service set
// once: ingress is already stopped, so no new agent service can be minted
// while it waits.
func (pm *PoolManager) WaitInFlight(ctx context.Context) error {
	pm.mu.RLock()
	services := make([]*Service, 0, len(pm.services))
	for _, svc := range pm.services {
		services = append(services, svc)
	}
	pm.mu.RUnlock()

	for _, svc := range services {
		if err := svc.Runtime.WaitTurns(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close shuts down all services and hook plugins.
func (pm *PoolManager) Close() error {
	pm.mu.Lock()
	services := pm.services
	pm.services = make(map[string]*Service)
	hookPlugins := pm.hookPlugins
	pm.hookPlugins = nil
	coreHooks := pm.coreHooks
	pm.coreHooks = nil
	pm.mu.Unlock()

	var lastErr error
	for id, svc := range services {
		pm.log.Info("closing agent service", "agent_id", id)
		if err := svc.Runtime.Close(); err != nil {
			pm.log.Error("failed to close service runtime", "agent_id", id, "error", err)
			lastErr = err
		}
	}
	pluginhooks.CloseHookPlugins(hookPlugins)
	// Core hooks (trace) are closed last so their end-of-session spans flush
	// after every runtime has stopped producing new ones.
	pluginhooks.CloseHookPlugins(coreHooks)
	if pm.mem != nil {
		if err := pm.mem.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
