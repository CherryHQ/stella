package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/tools"
)

// ExtraToolsFactory creates agent-specific extra tools given a snapshot.
// It allows callers to inject tools that depend on per-agent configuration
// (e.g. scheduler, memory retrieval).
type ExtraToolsFactory func(snap *config.Snapshot) []tools.Tool

// PluginToolsBuilder creates tools from enabled plugin state.
// Called at startup and on hot-reload when a plugin is toggled.
type PluginToolsBuilder func(ctx context.Context) []tools.Tool

// PluginHooksBuilder creates hook plugins from enabled plugin state.
// Called at startup and on hot-reload when a hook plugin is toggled.
type PluginHooksBuilder func(ctx context.Context) []hooks.HookPlugin

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

// WithExtraToolsFactory sets the function that creates per-agent extra tools.
func WithExtraToolsFactory(f ExtraToolsFactory) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.extraToolsFactory = f
	}
}

// WithSharedExtraTools sets tools shared across all agents (e.g. scheduler, memory).
func WithSharedExtraTools(tools []tools.Tool) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.coreSharedTools = tools
		pm.sharedExtraTools = tools
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

// PoolManager manages a map of agent ID to Pool. It reads enabled agents
// from the config Store and creates one Pool per agent.
type PoolManager struct {
	pools              map[string]*Pool
	store              config.Store
	mem                memory.Engine
	mu                 sync.RWMutex
	idleTimeout        time.Duration
	compaction         CompactionConfig
	coreSharedTools    []tools.Tool       // always-on tools (scheduler, memory, etc.)
	sharedExtraTools   []tools.Tool       // coreSharedTools + plugin tools
	pluginToolsBuilder PluginToolsBuilder // builds tools from plugin state
	hookPlugins        []hooks.HookPlugin // current enabled hook plugins
	pluginHooksBuilder PluginHooksBuilder // builds hooks from plugin state
	extraToolsFactory  ExtraToolsFactory
	userMemory         *memory.UserMemoryStore // per-user memory for prompt injection
	log                *slog.Logger
}

// NewPoolManager creates a new PoolManager.
func NewPoolManager(store config.Store, mem memory.Engine, opts ...PoolManagerOption) *PoolManager {
	pm := &PoolManager{
		pools:       make(map[string]*Pool),
		store:       store,
		mem:         mem,
		userMemory:  memory.NewUserMemoryStore(store),
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
	// Build initial shared tools: core + plugin tools.
	if pm.pluginToolsBuilder != nil {
		pluginTools := pm.pluginToolsBuilder(ctx)
		pm.sharedExtraTools = mergeTools(pm.coreSharedTools, pluginTools)
	}

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
		WithUserMemory(pm.userMemory),
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

// ReloadPluginTools rebuilds the shared tool set from current plugin state
// and updates the runner factory for every pool. New sessions pick up the
// change immediately; existing runners continue until their session rotates.
func (pm *PoolManager) ReloadPluginTools(ctx context.Context) error {
	if pm.pluginToolsBuilder == nil {
		return nil
	}

	pluginTools := pm.pluginToolsBuilder(ctx)

	pm.mu.Lock()
	pm.sharedExtraTools = mergeTools(pm.coreSharedTools, pluginTools)
	pools := make(map[string]*Pool, len(pm.pools))
	for id, p := range pm.pools {
		pools[id] = p
	}
	pm.mu.Unlock()

	for agentID, pool := range pools {
		if err := pm.rebuildPoolFactory(ctx, agentID, pool); err != nil {
			pm.log.Error("failed to rebuild factory after plugin reload", "agent_id", agentID, "error", err)
		}
	}

	pm.log.Info("plugin tools reloaded", "plugin_tool_count", len(pluginTools))
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
	pm.hookPlugins = hookPlugins
	pools := make(map[string]*Pool, len(pm.pools))
	for id, p := range pm.pools {
		pools[id] = p
	}
	pm.mu.Unlock()

	for _, pool := range pools {
		pool.SetHooks(pm.HookPlugins)
	}

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
	for id, p := range pm.pools {
		pools[id] = p
	}
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
	return nil
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

// buildFactory creates a runner factory with all shared, plugin, and per-agent tools.
// Hooks are not part of the factory — they are stored on the Pool and injected
// via RunnerParams.HooksFn at runner-creation time.
func (pm *PoolManager) buildFactory(_ context.Context, snap *config.Snapshot) (runner.NewRunnerFunc, error) {
	pm.mu.RLock()
	shared := pm.sharedExtraTools
	pm.mu.RUnlock()

	var extraTools []tools.Tool
	extraTools = append(extraTools, shared...)

	cwd, _ := os.Getwd()
	extraTools = append(extraTools, skills.NewTool(config.AnnaHome(), snap.Workspace, cwd, 0))

	if pm.extraToolsFactory != nil {
		extraTools = append(extraTools, pm.extraToolsFactory(snap)...)
	}

	return NewRunnerFactory(snap, extraTools)
}

// mergeTools creates a new slice containing core tools followed by plugin tools.
func mergeTools(core, plugin []tools.Tool) []tools.Tool {
	merged := make([]tools.Tool, 0, len(core)+len(plugin))
	merged = append(merged, core...)
	merged = append(merged, plugin...)
	return merged
}

// Close shuts down all pools.
func (pm *PoolManager) Close() error {
	pm.mu.Lock()
	pools := pm.pools
	pm.pools = make(map[string]*Pool)
	pm.mu.Unlock()

	var lastErr error
	for id, pool := range pools {
		pm.log.Info("closing agent pool", "agent_id", id)
		if err := pool.Close(); err != nil {
			pm.log.Error("failed to close pool", "agent_id", id, "error", err)
			lastErr = err
		}
	}
	return lastErr
}
