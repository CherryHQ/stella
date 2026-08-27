package runtime

import (
	"github.com/CherryHQ/stella/internal/authz"
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
	turnAuthority  authz.Authority
	hasAuthority   bool
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

// WithTurnAuthority binds the original direct-human Authority to this turn.
// Callers must only use it at human ChatAdmitted ingress; workers and nested
// session paths deliberately omit it.
func WithTurnAuthority(authority authz.Authority) Option {
	return func(o *chatOptions) {
		o.turnAuthority = authority
		o.hasAuthority = authority.Valid()
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

// WithGroupWake attaches why this group turn was started. It reaches the model
// with the trigger and is never persisted: history records what was said, not
// which gate let this turn run.
func WithGroupWake(wake memory.GroupWake) Option {
	return func(o *chatOptions) {
		o.groupWake = wake
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
