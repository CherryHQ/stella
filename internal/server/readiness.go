package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// readyzPingTimeout bounds the DB liveness probe inside /readyz so a stalled
// database can never hang a Kubernetes readiness check.
const readyzPingTimeout = 2 * time.Second

// DBPinger is the minimal database liveness surface the /healthz, /readyz, and
// admin status probes need. *pgxpool.Pool satisfies it; the composition root
// injects the pool as this narrow port so the probes can ping the database but
// never reach an application query. Tests inject a fake to exercise the failure
// branch without a real database.
type DBPinger interface {
	Ping(context.Context) error
}

// readiness tracks process lifecycle state for the infrastructure probes
// (/healthz, /readyz) and carries the graceful-drain signal that streaming
// handlers watch so they end their responses when shutdown begins.
//
// started and draining are atomics so a probe served on one goroutine observes
// state written by the shutdown orchestrator on another. beginDrain sets
// draining before it cancels drainCtx, and the orchestrator calls beginDrain
// before it touches the HTTP listener, so a concurrent probe can never see
// /readyz succeed after the drain has begun (see go-patterns "happens-before").
type readiness struct {
	started  atomic.Bool
	draining atomic.Bool
	ping     DBPinger
	storage  interface{ Check(context.Context) error }

	drainCtx    context.Context
	drainCancel context.CancelFunc
	drainOnce   sync.Once
}

// newReadiness builds readiness state whose drain signal is a child of parent
// (the server runtime context), so a hard process stop also releases streaming
// handlers even if graceful drain never ran.
func newReadiness(parent context.Context, ping DBPinger, storage interface{ Check(context.Context) error }) *readiness {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &readiness{ping: ping, storage: storage, drainCtx: ctx, drainCancel: cancel}
}

// markStartupComplete flips /readyz eligibility on once every subsystem has
// started. Until then /readyz reports 503 so orchestrators don't route to a
// half-initialized process.
func (r *readiness) markStartupComplete() { r.started.Store(true) }

// beginDrain starts graceful shutdown: it flips readiness to not-ready and then
// cancels the drain context so streaming handlers unwind. Idempotent — only the
// first call takes effect.
func (r *readiness) beginDrain() {
	r.drainOnce.Do(func() {
		r.draining.Store(true)
		r.drainCancel()
	})
}

// draining reports whether graceful drain has begun.
func (r *readiness) isDraining() bool { return r.draining.Load() }

// streamContext derives a context from parent that is also cancelled when the
// drain signal fires. A streaming handler passes its request context through
// this so its existing <-ctx.Done() branch ends the SSE response cleanly on
// graceful shutdown instead of blocking until the HTTP shutdown budget elapses.
// The returned cancel must be called (defer) to release the drain hook.
func (r *readiness) streamContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(r.drainCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// healthz is a liveness probe: 200 as long as the process is running and can
// serve HTTP. It intentionally checks nothing else so a transient dependency
// outage never triggers a pod restart.
func (r *readiness) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// readyz is a readiness probe: 200 only when startup is complete, drain has not
// begun, and the database answers a bounded ping. Any failing condition returns
// 503 with a body naming the reason so operators and probe logs can diagnose it.
func (r *readiness) readyz(w http.ResponseWriter, req *http.Request) {
	if !r.started.Load() {
		http.Error(w, "starting up", http.StatusServiceUnavailable)
		return
	}
	if r.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), readyzPingTimeout)
	defer cancel()
	if err := r.ping.Ping(ctx); err != nil {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	if r.storage != nil {
		if err := r.storage.Check(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
