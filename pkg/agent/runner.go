package agent

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

// Runner is a configured agent loop executor. It is safe for concurrent use.
// All configuration is set at construction time via RunnerConfig + options.
type Runner struct {
	stream             providers.StreamFunc
	model              ai.Model
	streamOptions      ai.StreamOptions
	tools              ToolSet
	toolDefs           []ai.ToolDefinition
	system             string
	interrupt          <-chan struct{}
	hooks              *hooks.HookSet
	hookMeta           hooks.HookMeta
	toolLifecycle      *ToolLifecycle
	imageText          ImageTextFunc
	mediaLoader        MediaLoader
	toolTransform      ToolResultTransform
	projectionObserver ProjectionObserver
	imageProjection    bool
	turnNotify         func(turn int, elapsed time.Duration) *string
}

// RunnerConfig holds the required fields for constructing a Runner.
type RunnerConfig struct {
	Stream          providers.StreamFunc
	Model           ai.Model
	Tools           ToolSet
	ToolDefinitions []ai.ToolDefinition
}

// Option configures optional Runner fields.
type Option func(*Runner)

// WithStreamOptions sets stream options (temperature, headers, etc.).
func WithStreamOptions(opts ai.StreamOptions) Option {
	return func(r *Runner) { r.streamOptions = opts }
}

// WithSystem sets the system prompt.
func WithSystem(s string) Option {
	return func(r *Runner) { r.system = s }
}

// WithInterrupt sets an interrupt channel to stop the loop.
func WithInterrupt(ch <-chan struct{}) Option {
	return func(r *Runner) { r.interrupt = ch }
}

// WithHooks sets the hook set and metadata for the loop.
func WithHooks(hs *hooks.HookSet, meta hooks.HookMeta) Option {
	return func(r *Runner) {
		r.hooks = hs
		r.hookMeta = meta
	}
}

// WithToolLifecycle sets optional tool lifecycle hooks for the loop.
func WithToolLifecycle(tl *ToolLifecycle) Option {
	return func(r *Runner) {
		r.toolLifecycle = tl
	}
}

// WithImageText sets the renderer used to replace inline images with text when
// the configured model cannot accept image input. Without it, images are sent
// as-is — the right default for a model whose capability was never declared.
func WithImageText(fn ImageTextFunc) Option {
	return func(r *Runner) { r.imageText = fn }
}

// WithMediaLoader sets the scoped immutable-media loader for active references.
func WithMediaLoader(loader MediaLoader) Option {
	return func(r *Runner) { r.mediaLoader = loader }
}

// WithToolResultTransform sets the canonicalizer for final tool results.
func WithToolResultTransform(transform ToolResultTransform) Option {
	return func(r *Runner) { r.toolTransform = transform }
}

// WithProjectionObserver observes aggregate image projection metrics.
func WithProjectionObserver(observer ProjectionObserver) Option {
	return func(r *Runner) { r.projectionObserver = observer }
}

// WithLegacyImageProjection preserves the pre-canonical request adapter for
// deferred group history. Ordinary sessions must not use it.
func WithLegacyImageProjection() Option {
	return func(r *Runner) { r.imageProjection = false }
}

// WithTurnNotify sets a callback invoked at the start of each turn.
// If it returns a non-nil string, that text is injected as a UserMessage
// before the model call for that turn.
func WithTurnNotify(fn func(turn int, elapsed time.Duration) *string) Option {
	return func(r *Runner) { r.turnNotify = fn }
}

// NewRunner constructs a Runner with the given config and options.
func NewRunner(cfg RunnerConfig, opts ...Option) (*Runner, error) {
	if cfg.Stream == nil {
		return nil, errors.New("agent: stream is required")
	}
	// Defensive copies: callers must not mutate after construction.
	toolsCopy := make(ToolSet, len(cfg.Tools))
	maps.Copy(toolsCopy, cfg.Tools)
	defsCopy := make([]ai.ToolDefinition, len(cfg.ToolDefinitions))
	copy(defsCopy, cfg.ToolDefinitions)

	r := &Runner{
		stream:          cfg.Stream,
		model:           cfg.Model,
		imageProjection: true,
		tools:           toolsCopy,
		toolDefs:        defsCopy,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// SetHookMeta updates the hook metadata (session/agent/user context).
// Safe to call between Run invocations; not safe during a Run.
func (r *Runner) SetHookMeta(meta hooks.HookMeta) {
	r.hookMeta = meta
}

// SetTurnNotify updates the turn notify callback.
// Safe to call between Run invocations; not safe during a Run.
func (r *Runner) SetTurnNotify(fn func(turn int, elapsed time.Duration) *string) {
	r.turnNotify = fn
}

// Run executes the agent loop from scratch. Its backward-compatible boundary
// treats every supplied message as active.
func (r *Runner) Run(ctx context.Context, messages []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	return r.RunWithActiveStart(ctx, messages, 0, emit)
}

// RunWithActiveStart executes a loop with an explicit boundary between
// assembled history and the current turn. activeStart indexes messages before
// any synthetic progress nudges are inserted.
func (r *Runner) RunWithActiveStart(ctx context.Context, messages []ai.Message, activeStart int, emit func(LoopEvent)) ([]ai.Message, error) {
	return run(ctx, r.loopConfig(), messages, activeStart, emit)
}

// Continue validates the history tail and resumes the agent loop.
func (r *Runner) Continue(ctx context.Context, messages []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("cannot continue empty history")
	}
	switch messages[len(messages)-1].(type) {
	case ai.UserMessage, ai.ToolResultMessage:
		return r.RunWithActiveStart(ctx, messages, len(messages)-1, emit)
	default:
		return nil, errors.New("invalid transcript tail for continue")
	}
}

func (r *Runner) loopConfig() loopConfig {
	return loopConfig{
		Stream:             r.stream,
		Model:              r.model,
		StreamOptions:      r.streamOptions,
		Tools:              r.tools,
		ToolDefinitions:    r.toolDefs,
		System:             r.system,
		Interrupt:          r.interrupt,
		Hooks:              r.hooks,
		HookMeta:           r.hookMeta,
		ToolLifecycle:      r.toolLifecycle,
		ImageText:          r.imageText,
		MediaLoader:        r.mediaLoader,
		ToolTransform:      r.toolTransform,
		ProjectionObserver: r.projectionObserver,
		ImageProjection:    r.imageProjection,
		TurnNotify:         r.turnNotify,
	}
}
