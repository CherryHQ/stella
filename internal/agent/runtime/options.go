package runtime

import (
	"context"

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
	privateHuman   bool
}

type privateHumanTurnKey struct{}

// WithPrivateHumanTurn marks a Chat call as a private turn initiated by a
// human. Only trusted Web and channel entry adapters may set this option;
// webhook, scheduler, task, and delegate paths must leave it unset.
func WithPrivateHumanTurn() Option {
	return func(o *chatOptions) {
		o.privateHuman = true
	}
}

func withPrivateHumanTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateHumanTurnKey{}, true)
}

func privateHumanTurnFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	allowed, _ := ctx.Value(privateHumanTurnKey{}).(bool)
	return allowed
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
