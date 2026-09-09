package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/runcontrol"
)

// ErrCompletionUnbound means that an adapter asked to use a completion
// barrier before Runtime admitted the corresponding AgentRun. A barrier is
// deliberately fail-closed: callers must not turn an unbound run into a
// successful durable result.
var ErrCompletionUnbound = errors.New("agent run completion barrier is unbound")

// CompletionBarrier is the source-adapter half of one runtime completion.
// Runtime binds it to the immutable lease before starting slow work. The
// adapter owns Ack after its final durable side effect; Runtime owns the
// fallback Ack for calls that have no external adapter.
type CompletionBarrier struct {
	mu sync.Mutex

	completion runcontrol.Completion
	bound      bool
	guard      func(context.Context) context.Context
	err        error
	done       chan struct{}
	doneOnce   sync.Once
}

// NewCompletionBarrier returns an unbound barrier. Runtime will bind it during
// admission, or close it with an admission error when admission fails.
func NewCompletionBarrier() *CompletionBarrier {
	return &CompletionBarrier{done: make(chan struct{})}
}

// WithCompletionBarrier attaches an adapter-owned completion barrier to one
// runtime admission.
func WithCompletionBarrier(barrier *CompletionBarrier) Option {
	return func(o *chatOptions) {
		o.completion = barrier
		o.completionExternal = barrier != nil
	}
}

// Bound reports whether Runtime has attached a durable completion authority.
func (b *CompletionBarrier) Bound() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bound
}

// Context validates the bound lease and returns the caller's context carrying
// the same ownership guard. It preserves the caller's deadline and cancel;
// lease lifetime belongs to the Store and is exposed through Completion.Done.
func (b *CompletionBarrier) Context(ctx context.Context) (context.Context, error) {
	if b == nil {
		return nil, ErrCompletionUnbound
	}
	b.mu.Lock()
	completion := b.completion
	bound := b.bound
	err := b.err
	guard := b.guard
	b.mu.Unlock()
	if !bound || completion == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrCompletionUnbound
	}
	if guard != nil {
		ctx = guard(ctx)
	}
	if err := completion.Check(ctx); err != nil {
		return nil, err
	}
	return ctx, nil
}

// Check validates the same owner used by source-domain writes and final Ack.
func (b *CompletionBarrier) Check(ctx context.Context) error {
	_, err := b.Context(ctx)
	return err
}

// Ack records the adapter's known external outcome. Repeating the same
// outcome is idempotent; a different outcome remains a conflict owned by the
// durable completion authority.
func (b *CompletionBarrier) Ack(ctx context.Context, outcome runcontrol.Outcome) error {
	if b == nil {
		return ErrCompletionUnbound
	}
	if !outcome.Valid() {
		return runcontrol.ErrInvalidOutcome
	}
	b.mu.Lock()
	completion := b.completion
	bound := b.bound
	err := b.err
	guard := b.guard
	b.mu.Unlock()
	if !bound || completion == nil {
		if err != nil {
			return err
		}
		return ErrCompletionUnbound
	}
	if guard != nil {
		ctx = guard(ctx)
	}
	return completion.Ack(ctx, outcome)
}

// Done closes after a successful durable Ack or a failed admission. It never
// closes merely because a caller abandoned the stream.
func (b *CompletionBarrier) Done() <-chan struct{} {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	return done
}

// bind is called by Runtime exactly once after AgentRun admission. The
// no-store completion used by standalone runtime tests still obeys the same
// barrier protocol; production callers receive a Lease.Completion instead.
func (b *CompletionBarrier) bind(completion runcontrol.Completion) error {
	return b.bindWithGuard(completion, nil)
}

func (b *CompletionBarrier) bindWithGuard(completion runcontrol.Completion, guard func(context.Context) context.Context) error {
	if b == nil {
		return ErrCompletionUnbound
	}
	if completion == nil {
		return errors.New("agent run completion is nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bound {
		if b.completion == completion {
			return nil
		}
		return errors.New("agent run completion barrier already bound")
	}
	if b.err != nil {
		return b.err
	}
	b.completion = completion
	b.guard = guard
	b.bound = true
	// Done must be stable even when an async consumer captures it before
	// admission binds the durable completion. Relay the lease's terminal signal
	// onto the barrier-owned channel instead of swapping channels under the
	// consumer. A source completion is required to expose Done; a nil channel
	// would otherwise leave the barrier permanently open.
	if boundDone := completion.Done(); boundDone != nil {
		go func() {
			<-boundDone
			b.signalDone()
		}()
	}
	return nil
}

// bindLease binds the barrier to a concrete Lease, preserving its immutable
// guard when callers use Context for source-domain durable writes.
func (b *CompletionBarrier) bindLease(lease *agentrun.Lease) error {
	if lease == nil {
		return ErrCompletionUnbound
	}
	// Install the completion and its immutable context bridge under one lock.
	// Bound must never become observable in the small interval before guard is
	// attached, otherwise Context could return an unguarded write context.
	return b.bindWithGuard(lease.Completion(), lease.ContextWith)
}

// fail closes an unbound barrier when admission cannot start. The error is
// retained so an adapter can distinguish failed admission from a successful
// but unacknowledged run while Done still cannot hang forever.
func (b *CompletionBarrier) fail(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bound {
		return
	}
	b.err = err
	b.signalDone()
}

func (b *CompletionBarrier) signalDone() {
	b.doneOnce.Do(func() { close(b.done) })
}

// noopCompletion is used only when Runtime is deliberately constructed
// without a Store, as in package tests. It keeps the adapter API deterministic
// without pretending to provide a cross-process ownership fence.
type noopCompletion struct{ done chan struct{} }

func (n *noopCompletion) Check(context.Context) error { return nil }
func (n *noopCompletion) Ack(context.Context, runcontrol.Outcome) error {
	select {
	case <-n.done:
	default:
		close(n.done)
	}
	return nil
}
func (n *noopCompletion) Done() <-chan struct{} { return n.done }
