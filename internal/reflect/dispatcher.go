package reflect

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

// DispatcherDeps holds the process-global, immutable collaborators reflect
// needs to run a review cycle. They are wired up once in gateway boot and
// shared across every per-fire Service instance.
type DispatcherDeps struct {
	Memory     memory.Provider
	Store      Store
	SkillStore pkgplugins.SkillStore
	Notifier   pkgplugins.Notifier
	StateStore pkgplugins.StateStore
	Workspace  string
	Providers  func(api, apiKey, baseURL string) (providers.StreamFunc, error)
	Log        *slog.Logger
}

// Dispatcher adapts the reflect Service into a scheduler.OnJobFunc. The
// scheduler fires Handle once per org per tick; Handle injects the org ID
// into ctx and runs a single review cycle.
type Dispatcher struct {
	deps DispatcherDeps
}

// NewDispatcher wires a Dispatcher with the given shared deps. Returns an
// error if any required dep is missing — fail fast at boot rather than
// nil-deref on the first tick.
func NewDispatcher(deps DispatcherDeps) (*Dispatcher, error) {
	if deps.Memory == nil {
		return nil, fmt.Errorf("reflect: Memory provider is required")
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("reflect: Store is required")
	}
	if deps.StateStore == nil {
		return nil, fmt.Errorf("reflect: StateStore is required")
	}
	if deps.Providers == nil {
		return nil, fmt.Errorf("reflect: Providers builder is required")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Dispatcher{deps: deps}, nil
}

// Handle implements scheduler.OnJobFunc. It derives an org-scoped context
// from job.OrgID, builds a Service backed by the dispatcher's shared deps,
// and runs one review cycle. Cycle-level failures (missing org, store
// errors) propagate so the scheduler records the run as errored. Per-agent
// failures are still logged inside the service and do not fail the run.
func (d *Dispatcher) Handle(ctx context.Context, job scheduler.Job) error {
	if job.OrgID == "" {
		return fmt.Errorf("reflect: scheduler job %q has no OrgID", job.Name)
	}
	orgCtx := config.WithOrgID(ctx, job.OrgID)

	svc := New(Config{
		StateStore: d.deps.StateStore,
		Memory:     d.deps.Memory,
		Store:      d.deps.Store,
		SkillStore: d.deps.SkillStore,
		Notifier:   d.deps.Notifier,
		Workspace:  d.deps.Workspace,
		Log:        d.deps.Log.With("org_id", job.OrgID),
		Providers:  d.deps.Providers,
	})
	return svc.RunOnce(orgCtx)
}
