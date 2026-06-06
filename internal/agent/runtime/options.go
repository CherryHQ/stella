package runtime

import "github.com/CherryHQ/stella/internal/memory"

// Option configures a single Chat call.
type Option func(*chatOptions)

type chatOptions struct {
	model          string
	systemOverride string
	excludedTools  []string
	currentSpeaker memory.CurrentSpeaker
	hasSpeaker     bool
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
