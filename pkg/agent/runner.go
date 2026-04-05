package agent

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/providers"
)

// Runner is a configured agent loop executor. It is safe for concurrent use.
// All configuration is set at construction time via RunnerConfig + options.
type Runner struct {
	providers     providers.ProviderGetter
	model         ai.Model
	streamOptions ai.StreamOptions
	maxTurns      int
	tools         ToolSet
	toolDefs      []ai.ToolDefinition
	system        string
	interrupt     <-chan struct{}
	hooks         *hooks.HookSet
	hookMeta      hooks.HookMeta
}

// RunnerConfig holds the required fields for constructing a Runner.
type RunnerConfig struct {
	Providers providers.ProviderGetter
	Model     ai.Model
	Tools     ToolSet
	ToolDefs  []ai.ToolDefinition
}

// Option configures optional Runner fields.
type Option func(*Runner)

// WithStreamOptions sets stream options (API key, base URL, etc.).
func WithStreamOptions(opts ai.StreamOptions) Option {
	return func(r *Runner) { r.streamOptions = opts }
}

// WithMaxTurns sets the maximum number of loop turns.
func WithMaxTurns(n int) Option {
	return func(r *Runner) { r.maxTurns = n }
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

// NewRunner constructs a Runner with the given config and options.
func NewRunner(cfg RunnerConfig, opts ...Option) (*Runner, error) {
	if cfg.Providers == nil {
		return nil, errors.New("agent: providers is required")
	}
	r := &Runner{
		providers: cfg.Providers,
		model:     cfg.Model,
		tools:     cfg.Tools,
		toolDefs:  cfg.ToolDefs,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Run executes the agent loop from scratch.
func (r *Runner) Run(ctx context.Context, messages []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	return run(ctx, r.loopConfig(), r.providers, messages, emit)
}

// Continue validates the history tail and resumes the agent loop.
func (r *Runner) Continue(ctx context.Context, messages []ai.Message, emit func(LoopEvent)) ([]ai.Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("cannot continue empty history")
	}
	switch messages[len(messages)-1].(type) {
	case ai.UserMessage, ai.ToolResultMessage:
		return r.Run(ctx, messages, emit)
	default:
		return nil, errors.New("invalid transcript tail for continue")
	}
}

func (r *Runner) loopConfig() loopConfig {
	return loopConfig{
		Model:           r.model,
		StreamOptions:   r.streamOptions,
		MaxTurns:        r.maxTurns,
		Tools:           r.tools,
		ToolDefinitions: r.toolDefs,
		System:          r.system,
		Interrupt:       r.interrupt,
		Hooks:           r.hooks,
		HookMeta:        r.hookMeta,
	}
}
