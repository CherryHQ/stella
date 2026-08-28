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
	stream          providers.StreamFunc
	model           ai.Model
	streamOptions   ai.StreamOptions
	tools           ToolSet
	toolDefs        []ai.ToolDefinition
	toolMode        ToolMode
	codeToolSurface CodeToolSurface
	system          string
	interrupt       <-chan struct{}
	hooks           *hooks.HookSet
	hookMeta        hooks.HookMeta
	toolLifecycle   *ToolLifecycle
	canonicalImages *CanonicalImageConfig
	secretValues    []string
	turnNotify      func(turn int, elapsed time.Duration) *string
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

// WithToolMode selects the internal loop strategy. Native is the construction
// default; deployments opt into code through STELLA_AGENT_TOOL_MODE.
func WithToolMode(mode ToolMode) Option {
	return func(r *Runner) { r.toolMode = mode }
}

// WithCodeToolSurface selects the provider-visible subset used by Code Mode.
// The zero value keeps the production hot-tool surface.
func WithCodeToolSurface(surface CodeToolSurface) Option {
	return func(r *Runner) {
		if surface == "" {
			surface = CodeToolSurfaceHot
		}
		r.codeToolSurface = surface
	}
}

// WithCanonicalImages enables the complete durable ordinary-session image
// policy. Both callbacks are required so hydration and tool canonicalization
// cannot be configured independently.
func WithCanonicalImages(cfg CanonicalImageConfig) Option {
	return func(r *Runner) { r.canonicalImages = &cfg }
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
		tools:           toolsCopy,
		toolDefs:        defsCopy,
		toolMode:        ToolModeNative,
		codeToolSurface: CodeToolSurfaceHot,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.toolMode != ToolModeNative && r.toolMode != ToolModeCode {
		return nil, errors.New("agent: invalid tool mode")
	}
	if r.codeToolSurface != CodeToolSurfaceHot && r.codeToolSurface != CodeToolSurfaceBash && r.codeToolSurface != CodeToolSurfaceOnly {
		return nil, errors.New("agent: invalid code tool surface")
	}
	if r.canonicalImages != nil {
		if r.canonicalImages.Load == nil || r.canonicalImages.CanonicalizeToolResult == nil {
			return nil, errors.New("agent: canonical image loader and tool canonicalizer are required together")
		}
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

// SetSecretValues replaces the exact runtime credentials removed from
// script-visible child event arguments. Safe between Run invocations only.
func (r *Runner) SetSecretValues(values []string) {
	r.secretValues = append([]string(nil), values...)
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
		Stream:          r.stream,
		Model:           r.model,
		StreamOptions:   r.streamOptions,
		Tools:           r.tools,
		ToolDefinitions: r.toolDefs,
		ToolMode:        r.toolMode,
		CodeToolSurface: r.codeToolSurface,
		System:          r.system,
		Interrupt:       r.interrupt,
		Hooks:           r.hooks,
		HookMeta:        r.hookMeta,
		ToolLifecycle:   r.toolLifecycle,
		CanonicalImages: r.canonicalImages,
		SecretValues:    append([]string(nil), r.secretValues...),
		TurnNotify:      r.turnNotify,
	}
}
