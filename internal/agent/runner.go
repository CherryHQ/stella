package agent

import (
	"context"
	"fmt"
	"time"

	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolUseEvent describes a tool invocation in progress or completed.
type ToolUseEvent struct {
	ID        string         // provider-assigned tool call ID
	Tool      string         // tool name, e.g. "bash", "read"
	Status    string         // "running", "done", "error"
	Input     string         // short summary of the tool input
	Arguments map[string]any // raw tool arguments
	Detail    string         // error detail or result summary (for "error" status)
	Content   string         // full tool result text (for "done"/"error" status)
}

// StepEvent marks the boundary of an agentic step (one LLM call + tool executions).
type StepEvent struct {
	Kind string // "start" or "finish"
}

// ImageEvent carries a base64-encoded image to be sent to the channel.
type ImageEvent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/jpeg"
}

// FileEvent carries a local file path to be sent to the channel.
type FileEvent struct {
	Path string // absolute path on disk
	Name string // display filename (with extension)
}

// Event is the consumer-facing stream event. Channels read these from the
// stream returned by Pool.Chat().
type Event struct {
	Text      string
	Reasoning string
	Image     *ImageEvent
	File      *FileEvent
	ToolUse   *ToolUseEvent
	Step      *StepEvent
	Store     ai.Message // if non-nil, Pool appends to session history
	Err       error
}

// MessageContent is the type for user messages passed through the runner pipeline.
// It is either string (text-only) or []ai.ContentBlock (multimodal, e.g. text + images).
type MessageContent = any

// RunnerParams holds parameters for creating a new Runner instance.
type RunnerParams struct {
	Model          string                     // model ID (empty = use default)
	Memory         any                        // memory.Provider — typed as any to avoid circular imports
	UserID         string                     // auth user ID for user-scoped runner creation
	SessionID      string                     // current Stella session ID for sandbox helpers
	AgentID        string                     // agent ID for profile loading
	ProjectID      string                     // associated project ID (empty = no project)
	HooksFn        func() []hooks.HookPlugin  // resolved at runner-creation time; nil = no hooks
	ExtraTools     []tools.Tool               // additional tools appended to the runner's registry
	DelegateRunner delegatetool.SessionRunner // persistent delegate session runner
}

// Runner runs prompts against an AI backend and exposes lifecycle methods.
type Runner interface {
	Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event
	Alive() bool
	Busy() bool
	LastActivity() time.Time
	SystemPrompt() string
	Close() error
}

// NewRunnerFunc creates a new Runner instance with the given params.
type NewRunnerFunc func(ctx context.Context, params RunnerParams) (Runner, error)

// MessageText extracts and joins all text from a message.
func MessageText(message MessageContent) string {
	switch m := message.(type) {
	case string:
		return m
	case []ai.ContentBlock:
		return ai.FlattenText(m)
	default:
		return fmt.Sprintf("%v", message)
	}
}
