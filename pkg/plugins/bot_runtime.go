package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type BotRuntimeDeps[T any] struct {
	Parent          context.Context
	Handler         pkgchannel.Handler
	Notifier        ChannelRegistry
	Log             *slog.Logger
	Now             func() time.Time
	Platform        string
	DecodeConfig    func(map[string]any) (T, error)
	ConfigureConfig func(T, PluginState) T
	ValidateConfig  func(T) string
	NewChannel      func(T, pkgchannel.Handler) (pkgchannel.Channel, error)
	Snapshot        func(time.Time, RuntimeState, string, T) RuntimeStatus
	// WrapHandler, when set, wraps the handler passed to NewChannel so its
	// context-bearing agent-facing methods run on the runtime's operation context
	// (a child of Parent that outlives the poll context) rather than the poller's
	// Start context. This is what lets Quiesce stop polling without cancelling work
	// already accepted. When nil, the handler is passed through unchanged.
	WrapHandler func(pkgchannel.Handler, context.Context) pkgchannel.Handler
}

type botManagedRuntime[T any] struct {
	deps BotRuntimeDeps[T]

	mu         sync.RWMutex
	generation int
	// quiesced marks a terminal drain: once set, Reconcile no longer restarts the
	// channel, so a late desired-state apply cannot undo the drain.
	quiesced bool
	// opCancel cancels the operation context. Accepted work runs on it, so it is
	// detached from Parent cancellation, left alive through Quiesce and River
	// drain, and cancelled only by Stop or the next Reconcile.
	opCancel context.CancelFunc
	// pollCancel cancels the poll context (a child of the operation context) passed
	// to Channel.Start. Quiesce cancels it to stop ingress while the operation
	// context — and any accepted work on it — keeps running.
	pollCancel  context.CancelFunc
	snapshot    RuntimeStatus
	channelName string
	channel     pkgchannel.Channel
}

type runtimeFinalizer interface {
	Finalize()
}

func finalizeChannel(ch pkgchannel.Channel) {
	if finalizer, ok := ch.(runtimeFinalizer); ok {
		finalizer.Finalize()
	}
}

func NewBotManagedRuntime[T any](deps BotRuntimeDeps[T]) *botManagedRuntime[T] {
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.DecodeConfig == nil {
		panic("plugins: missing bot runtime config decoder")
	}
	if deps.ValidateConfig == nil {
		deps.ValidateConfig = func(T) string { return "" }
	}
	if deps.NewChannel == nil {
		panic("plugins: missing bot runtime channel factory")
	}
	if deps.Snapshot == nil {
		panic("plugins: missing bot runtime snapshot builder")
	}
	return &botManagedRuntime[T]{
		deps: deps,
		snapshot: RuntimeStatus{
			State:     RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *botManagedRuntime[T]) Start(ctx context.Context, desired PluginState) error {
	return r.Reconcile(ctx, desired)
}

func (r *botManagedRuntime[T]) Apply(ctx context.Context, desired PluginState) error {
	return r.Reconcile(ctx, desired)
}

func (r *botManagedRuntime[T]) Reconcile(ctx context.Context, desired PluginState) error {
	cfg, err := r.deps.DecodeConfig(desired.Config)
	if err != nil {
		return err
	}
	if r.deps.ConfigureConfig != nil {
		cfg = r.deps.ConfigureConfig(cfg, desired)
	}

	r.mu.Lock()
	if r.quiesced {
		// Terminal drain: never restart. Report the rejected mutation rather than
		// pretending a desired-state apply succeeded during shutdown.
		r.mu.Unlock()
		return fmt.Errorf("%s runtime is quiescing", r.deps.Platform)
	}
	r.generation++
	generation := r.generation
	cancel := r.opCancel
	oldChannel := r.channel
	r.channel = nil
	r.opCancel = nil
	r.pollCancel = nil
	if cancel != nil {
		cancel()
	}
	finalizeChannel(oldChannel)
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(desired.ID)
		if r.channelName != "" && r.channelName != desired.ID {
			r.deps.Notifier.Unregister(r.channelName)
		}
	}
	if !desired.Enabled {
		r.channelName = ""
		r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateStopped, r.deps.Platform+" disabled", cfg)
		r.mu.Unlock()
		return nil
	}
	if message := r.deps.ValidateConfig(cfg); message != "" {
		r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateError, message, cfg)
		r.mu.Unlock()
		return nil
	}

	// Operation context: preserve Parent values but detach its cancellation. The
	// composition root cancels gctx before River finishes draining; accepted
	// channel work and notifier delivery must remain alive until the final runtime
	// Stop, which is deliberately deferred until after River.Stop.
	opCtx, opCancel := context.WithCancel(context.WithoutCancel(r.deps.Parent))
	handler := r.deps.Handler
	if r.deps.WrapHandler != nil {
		handler = r.deps.WrapHandler(handler, opCtx)
	}

	ch, err := r.deps.NewChannel(cfg, handler)
	if err != nil {
		opCancel()
		r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateError, err.Error(), cfg)
		r.mu.Unlock()
		return fmt.Errorf("build %s channel: %w", r.deps.Platform, err)
	}
	if ch.Name() == "" {
		opCancel()
		r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateError, r.deps.Platform+": empty channel name", cfg)
		r.mu.Unlock()
		return fmt.Errorf("build %s channel: empty channel name", r.deps.Platform)
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Register(ch)
	}
	pollCtx, pollCancel := context.WithCancel(opCtx)
	r.opCancel = opCancel
	r.pollCancel = pollCancel
	r.channelName = ch.Name()
	r.channel = ch
	r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateRunning, r.deps.Platform+" running", cfg)
	r.mu.Unlock()

	go func() {
		err := ch.Start(pollCtx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			// Superseded by Reconcile, Quiesce, or Stop. That path owns cleanup —
			// and Quiesce deliberately keeps the notifier registered — so this
			// return must not unregister or clear state.
			return
		}
		if r.deps.Notifier != nil {
			r.deps.Notifier.Unregister(ch.Name())
		}
		finalizeChannel(ch)
		if r.opCancel != nil {
			r.opCancel()
		}
		r.opCancel = nil
		r.pollCancel = nil
		r.channelName = ""
		r.channel = nil
		message := r.deps.Platform + " stopped"
		state := RuntimeStateStopped
		if err != nil && pollCtx.Err() == nil {
			message = err.Error()
			state = RuntimeStateError
			r.deps.Log.Error(r.deps.Platform+": stopped unexpectedly", "error", err)
		}
		r.snapshot = r.deps.Snapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

// Quiesce stops channel ingress for a graceful drain while preserving work
// already accepted. It is terminal (blocks any later Reconcile/restart) and
// idempotent. It bumps the generation so the running channel's Start-return
// cleanup no-ops — leaving the notifier registered so accepted work can still
// deliver — then cancels the poll context. Channel.Start's contract requires it
// to stop when that context is cancelled, so Quiesce must not also call Stop
// (some SDKs implement Stop as a one-shot handshake). It deliberately does NOT
// cancel the operation context or unregister the notifier; the final Stop does
// that.
func (r *botManagedRuntime[T]) Quiesce(ctx context.Context) {
	r.mu.Lock()
	if r.quiesced {
		r.mu.Unlock()
		return
	}
	r.quiesced = true
	r.generation++
	pollCancel := r.pollCancel
	r.pollCancel = nil
	r.mu.Unlock()

	// Cancel outside the runtime lock: the channel's Start-return cleanup also
	// takes r.mu to observe the generation change. The operation context remains
	// alive for accepted work and outbound delivery.
	if pollCancel != nil {
		pollCancel()
	}
}

func (r *botManagedRuntime[T]) Stop(ctx context.Context) error {
	var zero T

	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.opCancel != nil {
		r.opCancel()
		r.opCancel = nil
	}
	finalizeChannel(r.channel)
	r.channel = nil
	r.pollCancel = nil
	if r.deps.Notifier != nil {
		if r.channelName != "" {
			r.deps.Notifier.Unregister(r.channelName)
		} else {
			r.deps.Notifier.Unregister(r.deps.Platform)
		}
	}
	r.channelName = ""
	r.snapshot = r.deps.Snapshot(r.deps.Now(), RuntimeStateStopped, r.deps.Platform+" stopped", zero)
	return nil
}

func (r *botManagedRuntime[T]) Status(ctx context.Context) (RuntimeStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func (r *botManagedRuntime[T]) Snapshot(ctx context.Context) (RuntimeStatus, error) {
	return r.Status(ctx)
}
