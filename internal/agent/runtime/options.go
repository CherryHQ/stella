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
	sandboxResult   SandboxResultCallback
	stopWhen        func() bool
	closeAfterRun   bool
	model           string
	systemOverride  string
	excludedTools   []string
	allowedTools    []string
	hasAllowedTools bool
	extraTools      []tools.Tool
	currentSpeaker  memory.CurrentSpeaker
	hasSpeaker      bool
	inputActor      eventlog.MessageActor
	inboxID         string
	groupWake       memory.GroupWake
	channel         string
	runID           string
	bindingID       string
	turnAuthority   authz.Authority
	hasAuthority    bool
}

// WithInputActor attaches runtime-derived provenance to the input message.
// The value comes from trusted authority/session state, never model arguments.
func WithInputActor(actor eventlog.MessageActor) Option {
	return func(o *chatOptions) {
		o.inputActor = actor
	}
}

// WithTurnAuthority carries the capability minted by the trusted admission
// adapter. Runtime installs it only after clearing any inherited context value.
func WithTurnAuthority(authority authz.Authority) Option {
	return func(o *chatOptions) {
		o.turnAuthority = authority
		o.hasAuthority = authority.Valid()
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

// WithGroupWake attaches why this group turn was started. It reaches the model
// with the trigger and is never persisted: history records what was said, not
// which gate let this turn run.
func WithGroupWake(wake memory.GroupWake) Option {
	return func(o *chatOptions) {
		o.groupWake = wake
	}
}

// WithTelemetryChannel records the low-cardinality transport and its separate
// binding identifier in hook metadata without changing session routing.
func WithTelemetryChannel(channel, bindingID string) Option {
	return func(o *chatOptions) {
		o.channel = channel
		o.bindingID = bindingID
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

// WithAllowedTools restricts this Chat call to the named tools/families.
// Calling it with no names deliberately exposes no tools.
func WithAllowedTools(names ...string) Option {
	return func(o *chatOptions) {
		o.allowedTools = append(o.allowedTools, names...)
		o.hasAllowedTools = true
	}
}

// WithExtraTools binds additional tools to the runner for this Chat call.
// The runner is rebuilt for the call (per-call tools defeat the session
// cache). Use WithOneShotRunner for worker turns so their tools never reach a
// later tool-less turn.
func WithExtraTools(ts ...tools.Tool) Option {
	return func(o *chatOptions) {
		o.extraTools = append(o.extraTools, ts...)
	}
}

// WithStopWhen ends the model loop after a terminal tool result, while keeping
// the execution context alive for result checks and commits.
func WithStopWhen(done func() bool) Option {
	return func(o *chatOptions) { o.stopWhen = done }
}

// WithOneShotRunner closes this turn's runner before releasing admission, so a
// caller cannot accidentally close the next turn's runner after receiving EOF.
func WithOneShotRunner() Option {
	return func(o *chatOptions) { o.closeAfterRun = true }
}

// WithRunID links the turn's durable events to the agent_run row that drove
// it. Set only by the durable run worker; zero for synchronous turns.
func WithRunID(id string) Option {
	return func(o *chatOptions) {
		o.runID = id
	}
}
