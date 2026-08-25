package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Option configures a single Chat call.
type Option func(*chatOptions)

type chatOptions struct {
	model          string
	systemOverride string
	excludedTools  []string
	extraTools     []tools.Tool
	currentSpeaker memory.CurrentSpeaker
	hasSpeaker     bool
	inputActor     eventlog.MessageActor
	inboxID        string
	groupWake      memory.GroupWake
	channelFIFOID  string
	fifoClaimToken string
	completion     *CompletionBarrier
}

// CompletionBarrier keeps the AgentRun running after the event stream closes
// while an entry adapter commits its source-domain result. Source writers use
// Context so every mutation validates the same owner in its transaction, then
// Release lets the runtime commit session activity and the terminal Run state.
type CompletionBarrier struct {
	ready       chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	readyOnce   sync.Once
	releaseOnce sync.Once
	guard       agentrun.Guard
	guardCtx    context.Context
	err         error
}

func NewCompletionBarrier() *CompletionBarrier {
	return &CompletionBarrier{ready: make(chan struct{}), release: make(chan struct{})}
}

func (b *CompletionBarrier) bind(guardCtx context.Context, guard agentrun.Guard) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.guard = guard
	b.guardCtx = guardCtx
	b.mu.Unlock()
	b.readyOnce.Do(func() { close(b.ready) })
}

// Fail unblocks an entry adapter when admission failed before an AgentRun
// could bind a durable ownership guard.
func (b *CompletionBarrier) Fail(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
	b.readyOnce.Do(func() { close(b.ready) })
}

func (b *CompletionBarrier) Context(ctx context.Context) (context.Context, error) {
	if b == nil {
		return nil, fmt.Errorf("agent run completion barrier is nil")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ready:
	}
	b.mu.Lock()
	guard, guardCtx, err := b.guard, b.guardCtx, b.err
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if guard.RunID == "" {
		return nil, fmt.Errorf("agent run completion barrier has no guard")
	}
	if guarded, ok := agentrun.InheritGuard(ctx, guardCtx); ok {
		return guarded, nil
	}
	return nil, fmt.Errorf("agent run completion barrier lost its live ownership checker")
}

func (b *CompletionBarrier) Release() {
	if b == nil {
		return
	}
	b.releaseOnce.Do(func() { close(b.release) })
}

// Bound reports whether admission has resolved the barrier, either with an
// AgentRun guard or an admission error.
func (b *CompletionBarrier) Bound() bool {
	if b == nil {
		return false
	}
	select {
	case <-b.ready:
		return true
	default:
		return false
	}
}

// WithCompletionBarrier delays only terminalization; model/tool operations and
// transcript writes have already ended when the caller observes stream EOF.
func WithCompletionBarrier(barrier *CompletionBarrier) Option {
	return func(o *chatOptions) { o.completion = barrier }
}

// WithChannelFIFOClaim binds a claimed durable channel input to the Run in the
// same admission transaction.
func WithChannelFIFOClaim(id, claimToken string) Option {
	return func(o *chatOptions) {
		o.channelFIFOID = id
		o.fifoClaimToken = claimToken
	}
}

// WithInputActor attaches runtime-derived provenance to the input message.
// The value comes from trusted authority/session state, never model arguments.
func WithInputActor(actor eventlog.MessageActor) Option {
	return func(o *chatOptions) {
		o.inputActor = actor
	}
}

// WithInboxID binds a runtime-authored durable Session inbox row to this input.
// It is internal admission metadata, never a model argument.
func WithInboxID(id string) Option {
	return func(o *chatOptions) {
		o.inboxID = id
	}
}

// WithCurrentSpeaker attaches the per-turn group speaker for this Chat call.
// It is a personalization target only — the runtime never promotes it to the
// session/runtime identity (D9). DM turns leave it unset.
func WithCurrentSpeaker(speaker memory.CurrentSpeaker) Option {
	return func(o *chatOptions) {
		o.currentSpeaker = speaker
		o.hasSpeaker = true
	}
}

// WithGroupWake attaches why this group turn was started.
func WithGroupWake(wake memory.GroupWake) Option {
	return func(o *chatOptions) { o.groupWake = wake }
}

// WithModel overrides the model for this Chat call.
func WithModel(model string) Option {
	return func(o *chatOptions) {
		o.model = model
	}
}

// WithSystemOverride overrides the system prompt for this Chat call.
func WithSystemOverride(system string) Option {
	return func(o *chatOptions) {
		o.systemOverride = system
	}
}

// WithExcludedTools hides the named tools for this Chat call.
func WithExcludedTools(names ...string) Option {
	return func(o *chatOptions) {
		o.excludedTools = append(o.excludedTools, names...)
	}
}

// WithExtraTools binds additional tools to the runner for this Chat call.
// The runner is rebuilt for the call (per-call tools defeat the session
// cache), so callers should evict the session runner afterwards via
// CloseSession to avoid the tools leaking into later tool-less turns.
func WithExtraTools(ts ...tools.Tool) Option {
	return func(o *chatOptions) {
		o.extraTools = append(o.extraTools, ts...)
	}
}
