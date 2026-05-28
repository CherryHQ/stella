package reflect

import (
	"context"
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
	Store      pkgplugins.ReflectStore
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

// NewDispatcher wires a Dispatcher with the given shared deps.
func NewDispatcher(deps DispatcherDeps) *Dispatcher {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Dispatcher{deps: deps}
}

// Handle implements scheduler.OnJobFunc. It derives an org-scoped context
// from job.OrgID, builds a Service backed by the dispatcher's shared deps,
// and runs one review cycle. Errors are logged inside the service; we
// always return nil so the scheduler doesn't mark builtin runs as errored
// when a single agent's review fails.
func (d *Dispatcher) Handle(ctx context.Context, job scheduler.Job) error {
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
	svc.RunOnce(orgCtx)
	return nil
}
