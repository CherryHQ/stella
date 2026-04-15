package agent

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// BuiltinToolsFactory creates agent-specific builtin tools given a snapshot.
// It allows callers to inject always-on tools that depend on per-agent
// configuration (for example, notifications).
type BuiltinToolsFactory func(snap *config.Snapshot) []tools.Tool

// PluginToolsBuilder creates tools from enabled plugin state.
// Called per runner so tool builders receive the active sandbox host.
type PluginToolsBuilder func(ctx context.Context, build plugintools.BuildContext) []tools.Tool

// PluginHooksBuilder creates hook plugins from enabled plugin state.
// Called at startup and on hot-reload when a hook plugin is toggled.
type PluginHooksBuilder func(ctx context.Context) []hooks.HookPlugin

// PromptToolsBuilder returns structured prompt inventory for the active plugin host.
type (
	PromptToolsBuilder      func(ctx context.Context) ([]pkgplugins.PromptToolInfo, error)
	PromptSectionsBuilder   func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)
	BeforeRunBuilder        func(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error)
	ProviderRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error)
)

// PoolManagerOption configures a PoolManager.
type PoolManagerOption func(*PoolManager)

// WithIdleTimeoutPM sets the idle timeout for all pools.
func WithIdleTimeoutPM(d time.Duration) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.idleTimeout = d
	}
}

// WithCompactionPM sets the compaction config for all pools.
func WithCompactionPM(cfg CompactionConfig) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.compaction = cfg
	}
}

// WithBuiltinToolsFactory sets the function that creates per-agent builtin tools.
func WithBuiltinToolsFactory(f BuiltinToolsFactory) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.builtinToolsFactory = f
	}
}

// WithBuiltinTools sets the always-on builtin tools available to all agents.
func WithBuiltinTools(tools []tools.Tool) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.builtinTools = tools
	}
}

// WithPluginToolsBuilder sets the function that builds tools from plugin state.
func WithPluginToolsBuilder(b PluginToolsBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.pluginToolsBuilder = b
	}
}

// WithPluginHooksBuilder sets the function that builds hooks from plugin state.
func WithPluginHooksBuilder(b PluginHooksBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.pluginHooksBuilder = b
	}
}

// WithPromptToolsBuilder sets the function that returns prompt inventory items.
func WithPromptToolsBuilder(b PromptToolsBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.promptToolsBuilder = b
	}
}

func WithPromptSectionsBuilder(b PromptSectionsBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.promptSectionsBuilder = b
	}
}

func WithBeforeRunBuilderPM(b BeforeRunBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.beforeRunBuilder = b
	}
}

func WithToolLifecyclePM(tl *coreagent.ToolLifecycle) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.toolLifecycle = tl
	}
}

func WithProviderRegistryBuilder(b ProviderRegistryBuilder) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.providerRegistryBuilder = b
	}
}

// PoolManager manages a map of agent ID to Pool. It reads enabled agents
// from the config Store and creates one Pool per agent.
type PoolManager struct {
	pools                   map[string]*Pool
	store                   config.Store
	mem                     memory.Provider
	mu                      sync.RWMutex
	idleTimeout             time.Duration
	compaction              CompactionConfig
	builtinTools            []tools.Tool       // always-on builtin tools (scheduler, memory, etc.)
	pluginToolsBuilder      PluginToolsBuilder // builds external tools from enabled plugin state
	hookPlugins             []hooks.HookPlugin // current enabled hook plugins
	pluginHooksBuilder      PluginHooksBuilder // builds hooks from plugin state
	promptToolsBuilder      PromptToolsBuilder // builds prompt inventory from plugin state
	promptSectionsBuilder   PromptSectionsBuilder
	beforeRunBuilder        BeforeRunBuilder
	toolLifecycle           *coreagent.ToolLifecycle
	providerRegistryBuilder ProviderRegistryBuilder
	builtinToolsFactory     BuiltinToolsFactory
	log                     *slog.Logger
}

// NewPoolManager creates a new PoolManager.
func NewPoolManager(store config.Store, mem memory.Provider, opts ...PoolManagerOption) *PoolManager {
	pm := &PoolManager{
		pools:       make(map[string]*Pool),
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

// Get returns the Pool for the given agent ID, or nil if not found.
func (pm *PoolManager) Get(agentID string) *Pool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pools[agentID]
}

// HookPlugins returns a snapshot copy of the current enabled hook plugins.
// Returns a copy so callers cannot mutate or alias the internal slice.
func (pm *PoolManager) HookPlugins() []hooks.HookPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make([]hooks.HookPlugin, len(pm.hookPlugins))
	copy(out, pm.hookPlugins)
	return out
}

// StartAll reads enabled agents from the store, creates a Pool per agent
// with per-agent runner factory, and starts reapers.
func (pm *PoolManager) StartAll(ctx context.Context) error {
	// Build initial hook plugins.
	if pm.pluginHooksBuilder != nil {
		pm.hookPlugins = pm.pluginHooksBuilder(ctx)
	}

	agents, err := pm.store.ListEnabledAgents(ctx)
	if err != nil {
		return fmt.Errorf("list enabled agents: %w", err)
	}
	if len(agents) == 0 {
		return fmt.Errorf("no enabled agents found")
	}

	for _, ag := range agents {
		if err := pm.startAgent(ctx, ag); err != nil {
			pm.log.Error("failed to start agent", "agent_id", ag.ID, "error", err)
			continue
		}
	}

	pm.mu.RLock()
	count := len(pm.pools)
	pm.mu.RUnlock()

	if count == 0 {
		return fmt.Errorf("no agents could be started")
	}

	pm.log.Info("all agents started", "count", count)
	return nil
}

// startAgent sets up workspace, builds runner factory, creates pool, and starts reaper.
func (pm *PoolManager) startAgent(ctx context.Context, ag config.Agent) error {
	snap, workspace, err := pm.loadAgentSnapshot(ctx, ag.ID)
	if err != nil {
		return err
	}

	factory, err := pm.buildFactory(ctx, snap)
	if err != nil {
		return err
	}

	poolOpts := []PoolOption{
		WithAgentID(ag.ID),
		WithIdleTimeout(pm.idleTimeout),
		WithCompaction(pm.compaction.WithDefaults()),
		WithDefaultModel(snap.ResolveModelID(config.ModelTierStrong)),
		WithFastModel(snap.ResolveModelID(config.ModelTierFast)),
		WithBeforeRunBuilder(pm.beforeRunBuilder),
	}

	pool := NewPool(factory, pm.mem, poolOpts...)
	pool.SetHooks(pm.HookPlugins)
	go pool.StartReaper(ctx)

	pm.mu.Lock()
	pm.pools[ag.ID] = pool
	pm.mu.Unlock()

	pm.log.Info("agent started", "agent_id", ag.ID, "workspace", workspace)
	return nil
}

// DefaultPool returns the first pool found in the map, or nil if empty.
// Useful for backward compatibility with code expecting a single pool.
func (pm *PoolManager) DefaultPool() *Pool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, p := range pm.pools {
		return p
	}
	return nil
}

// ReloadPluginTools updates the runner factory for every pool. Plugin tools are
// built per runner so builders can receive the active sandbox host.
func (pm *PoolManager) ReloadPluginTools(ctx context.Context) error {
	if pm.pluginToolsBuilder == nil {
		return nil
	}

	pm.mu.RLock()
	pools := make(map[string]*Pool, len(pm.pools))
	maps.Copy(pools, pm.pools)
	pm.mu.RUnlock()

	for agentID, pool := range pools {
		if err := pm.rebuildPoolFactory(ctx, agentID, pool); err != nil {
			pm.log.Error("failed to rebuild factory after plugin reload", "agent_id", agentID, "error", err)
		}
	}

	pm.log.Info("plugin tools reloaded")
	return nil
}

// ReloadPluginHooks rebuilds the hook plugin set from current plugin state
// and propagates it to every pool. No factory rebuild is needed — hooks live
// on the Pool and are injected via RunnerParams at runner-creation time.
func (pm *PoolManager) ReloadPluginHooks(ctx context.Context) error {
	if pm.pluginHooksBuilder == nil {
		return nil
	}

	hookPlugins := pm.pluginHooksBuilder(ctx)

	pm.mu.Lock()
	oldPlugins := pm.hookPlugins
	pm.hookPlugins = hookPlugins
	pools := make(map[string]*Pool, len(pm.pools))
	maps.Copy(pools, pm.pools)
	pm.mu.Unlock()

	for _, pool := range pools {
		pool.SetHooks(pm.HookPlugins)
	}

	// Close old hook plugins that implement io.Closer (e.g. OTel exporter).
	pluginhooks.CloseHookPlugins(oldPlugins)

	pm.log.Info("plugin hooks reloaded", "hook_count", len(hookPlugins))
	return nil
}

// ReloadPluginProviders rebuilds the runner factory for every pool so new
// sessions pick up changed provider credentials or enabled state. Provider
// creds are resolved from the Snapshot at factory-build time, so a simple
// factory rebuild is sufficient.
func (pm *PoolManager) ReloadPluginProviders(ctx context.Context) error {
	pm.mu.RLock()
	pools := make(map[string]*Pool, len(pm.pools))
	maps.Copy(pools, pm.pools)
	pm.mu.RUnlock()

	for agentID, pool := range pools {
		if err := pm.rebuildPoolFactory(ctx, agentID, pool); err != nil {
			pm.log.Error("failed to rebuild factory after provider reload", "agent_id", agentID, "error", err)
		}
	}

	pm.log.Info("plugin providers reloaded")
	return nil
}

// rebuildPoolFactory rebuilds and replaces the runner factory for a single pool.
func (pm *PoolManager) rebuildPoolFactory(ctx context.Context, agentID string, pool *Pool) error {
	snap, _, err := pm.loadAgentSnapshot(ctx, agentID)
	if err != nil {
		return err
	}
	factory, err := pm.buildFactory(ctx, snap)
	if err != nil {
		return err
	}
	pool.SetFactory(factory)
	pool.SetDefaultModel(snap.ResolveModelID(config.ModelTierStrong))
	pool.SetFastModel(snap.ResolveModelID(config.ModelTierFast))
	return nil
}

// SyncAgent reloads one agent's pool configuration immediately. If the agent
// was deleted or disabled, its pool is closed and removed. If it exists and is
// enabled, an existing pool is rebuilt and its live session runners are reset
// so subsequent requests use the latest snapshot.
func (pm *PoolManager) SyncAgent(ctx context.Context, agentID string) error {
	ag, err := pm.store.GetAgent(ctx, agentID)
	if err != nil {
		return pm.removeAgentPool(agentID)
	}
	if !ag.Enabled {
		return pm.removeAgentPool(agentID)
	}

	pm.mu.RLock()
	pool := pm.pools[agentID]
	pm.mu.RUnlock()

	if pool == nil {
		return pm.startAgent(ctx, ag)
	}
	if err := pm.rebuildPoolFactory(ctx, agentID, pool); err != nil {
		return err
	}
	if err := pool.ResetRunners(); err != nil {
		pm.log.Warn("failed to reset agent runners after sync", "agent_id", agentID, "error", err)
	}
	pm.log.Info("agent pool reloaded", "agent_id", agentID)
	return nil
}

func (pm *PoolManager) removeAgentPool(agentID string) error {
	pm.mu.Lock()
	pool := pm.pools[agentID]
	delete(pm.pools, agentID)
	pm.mu.Unlock()
	if pool == nil {
		return nil
	}
	pm.log.Info("removing agent pool", "agent_id", agentID)
	return pool.closeSessions()
}

// loadAgentSnapshot loads the config snapshot for an agent and sets up its workspace.
func (pm *PoolManager) loadAgentSnapshot(ctx context.Context, agentID string) (*config.Snapshot, string, error) {
	workspace, err := SetupWorkspace(agentID, config.AnnaHome())
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

// buildFactory creates a runner factory with builtin tools and external plugin
// tools. Hooks are not part of the factory — they are stored on the Pool and
// injected via RunnerParams.HooksFn at runner-creation time.
func (pm *PoolManager) buildFactory(_ context.Context, snap *config.Snapshot) (runner.NewRunnerFunc, error) {
	pm.mu.RLock()
	builtinTools := append([]tools.Tool{}, pm.builtinTools...)
	pm.mu.RUnlock()

	if pm.builtinToolsFactory != nil {
		builtinTools = mergeTools(builtinTools, pm.builtinToolsFactory(snap))
	}

	return NewRunnerFactory(snap, builtinTools, pm.pluginToolsBuilder, pm.providerRegistryBuilder, pm.promptToolsBuilder, pm.promptSectionsBuilder, pm.toolLifecycle)
}

// mergeTools creates a new slice containing builtin tools followed by more
// builtin additions.
func mergeTools(builtin, additions []tools.Tool) []tools.Tool {
	merged := make([]tools.Tool, 0, len(builtin)+len(additions))
	merged = append(merged, builtin...)
	merged = append(merged, additions...)
	return merged
}

// Close shuts down all pools and hook plugins.
func (pm *PoolManager) Close() error {
	pm.mu.Lock()
	pools := pm.pools
	pm.pools = make(map[string]*Pool)
	hookPlugins := pm.hookPlugins
	pm.hookPlugins = nil
	pm.mu.Unlock()

	var lastErr error
	for id, pool := range pools {
		pm.log.Info("closing agent pool", "agent_id", id)
		if err := pool.closeSessions(); err != nil {
			pm.log.Error("failed to close pool", "agent_id", id, "error", err)
			lastErr = err
		}
	}
	pluginhooks.CloseHookPlugins(hookPlugins)
	if pm.mem != nil {
		if err := pm.mem.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
