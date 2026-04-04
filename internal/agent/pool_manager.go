package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/skills"
)

// ExtraToolsFactory creates agent-specific extra tools given a snapshot.
// It allows callers to inject tools that depend on per-agent configuration
// (e.g. scheduler, memory retrieval).
type ExtraToolsFactory func(snap *config.Snapshot) []agenttool.Tool

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
func WithSharedExtraTools(tools []agenttool.Tool) PoolManagerOption {
	return func(pm *PoolManager) {
		pm.sharedExtraTools = tools
	}
}

// PoolManager manages a map of agent ID to Pool. It reads enabled agents
// from the config Store and creates one Pool per agent.
type PoolManager struct {
	pools             map[string]*Pool
	store             config.Store
	mem               memory.Engine
	mu                sync.RWMutex
	idleTimeout       time.Duration
	compaction        CompactionConfig
	sharedExtraTools  []agenttool.Tool
	extraToolsFactory ExtraToolsFactory
	userMemory        *memory.UserMemoryStore // per-user memory for prompt injection
	log               *slog.Logger
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

// StartAll reads enabled agents from the store, creates a Pool per agent
// with per-agent runner factory, and starts reapers.
func (pm *PoolManager) StartAll(ctx context.Context) error {
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
	// Setup workspace.
	workspace, err := SetupWorkspace(ag.ID, config.AnnaHome())
	if err != nil {
		return fmt.Errorf("setup workspace: %w", err)
	}

	// Get snapshot for this agent.
	snap, err := pm.store.Snapshot(ctx, ag.ID)
	if err != nil {
		return fmt.Errorf("load snapshot for agent %q: %w", ag.ID, err)
	}
	// Override workspace to the per-agent workspace.
	snap.Workspace = workspace

	// Build extra tools: shared tools + per-agent skills tool.
	var extraTools []agenttool.Tool
	extraTools = append(extraTools, pm.sharedExtraTools...)

	// Per-agent skills tool (userID=0 for agent-level, replaced per-session when user is known).
	cwd, _ := os.Getwd()
	extraTools = append(extraTools, skills.NewTool(config.AnnaHome(), workspace, cwd, 0))

	// Extra tools from factory (caller-provided, e.g. agent-specific tools).
	if pm.extraToolsFactory != nil {
		extraTools = append(extraTools, pm.extraToolsFactory(snap)...)
	}

	// Create runner factory.
	factory, err := NewRunnerFactory(snap, extraTools)
	if err != nil {
		return fmt.Errorf("create runner factory for agent %q: %w", ag.ID, err)
	}

	// Build pool options.
	poolOpts := []PoolOption{
		WithAgentID(ag.ID),
		WithIdleTimeout(pm.idleTimeout),
		WithCompaction(pm.compaction.WithDefaults()),
		WithDefaultModel(snap.ResolveModelID(config.ModelTierStrong)),
		WithFastModel(snap.ResolveModelID(config.ModelTierFast)),
		WithUserMemory(pm.userMemory),
	}

	pool := NewPool(factory, pm.mem, poolOpts...)
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
