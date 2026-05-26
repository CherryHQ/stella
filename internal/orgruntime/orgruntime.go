package orgruntime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/CherryHQ/stella/internal/config"
)

// AgentSyncer syncs an agent's pool by ID using an org-scoped context.
type AgentSyncer interface {
	SyncAgent(ctx context.Context, agentID string) error
}

// ChannelStarter starts all managed channel runtimes for an org-scoped context.
type ChannelStarter interface {
	StartChannels(ctx context.Context) error
}

// OrgRuntime holds per-org lifecycle state while using shared infrastructure.
type OrgRuntime struct {
	orgID   string
	started bool
	mu      sync.Mutex
}

// OrgID returns the org's ID.
func (r *OrgRuntime) OrgID() string { return r.orgID }

// Start initializes runtime services for this org: syncs agent pools,
// starts channel runtimes, and ensures builtin scheduler jobs.
func (r *OrgRuntime) Start(ctx context.Context, store config.Store, syncer AgentSyncer, channels ChannelStarter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}

	orgCtx := config.WithOrgID(ctx, r.orgID)

	agents, err := store.ListEnabledAgents(orgCtx)
	if err != nil {
		return fmt.Errorf("orgruntime: list agents for org %s: %w", r.orgID, err)
	}
	for _, a := range agents {
		if err := syncer.SyncAgent(orgCtx, a.ID); err != nil {
			slog.Warn("orgruntime: sync agent failed", "org_id", r.orgID, "agent_id", a.ID, "error", err)
		}
	}

	if channels != nil {
		if err := channels.StartChannels(orgCtx); err != nil {
			slog.Warn("orgruntime: start channels failed", "org_id", r.orgID, "error", err)
		}
	}

	r.started = true
	slog.Info("orgruntime: started", "org_id", r.orgID, "agents", len(agents))
	return nil
}

// Manager creates and caches OrgRuntime instances. Thread-safe.
type Manager struct {
	mu       sync.Mutex
	runtimes map[string]*OrgRuntime

	store    config.Store
	syncer   AgentSyncer
	channels ChannelStarter
}

// ManagerDeps holds the shared dependencies injected into Manager.
type ManagerDeps struct {
	Store    config.Store
	Syncer   AgentSyncer
	Channels ChannelStarter
}

// NewManager creates a Manager with the given shared dependencies.
func NewManager(deps ManagerDeps) *Manager {
	return &Manager{
		runtimes: make(map[string]*OrgRuntime),
		store:    deps.Store,
		syncer:   deps.Syncer,
		channels: deps.Channels,
	}
}

// GetOrInit returns the cached OrgRuntime for orgID, or creates and starts one.
// It does NOT seed the org — the org must already exist with seeded data.
// Idempotent and safe for concurrent calls.
func (m *Manager) GetOrInit(ctx context.Context, orgID string) (*OrgRuntime, error) {
	m.mu.Lock()
	if rt, ok := m.runtimes[orgID]; ok {
		m.mu.Unlock()
		return rt, nil
	}

	rt := &OrgRuntime{orgID: orgID}
	m.runtimes[orgID] = rt
	m.mu.Unlock()

	if err := rt.Start(ctx, m.store, m.syncer, m.channels); err != nil {
		m.mu.Lock()
		delete(m.runtimes, orgID)
		m.mu.Unlock()
		return nil, fmt.Errorf("orgruntime: init org %s: %w", orgID, err)
	}

	return rt, nil
}

// EnsureStarted satisfies auth.OrgInitializer — starts the org runtime,
// discarding the *OrgRuntime value.
func (m *Manager) EnsureStarted(ctx context.Context, orgID string) error {
	_, err := m.GetOrInit(ctx, orgID)
	return err
}
