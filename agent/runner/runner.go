package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/vaayne/anna/ai"
)

// ToolUseEvent describes a tool invocation in progress or completed.
type ToolUseEvent struct {
	Tool   string // tool name, e.g. "bash", "read"
	Status string // "running", "done", "error"
	Input  string // short summary of the tool input
	Detail string // error detail or result summary (for "error" status)
}

// ImageEvent carries a base64-encoded image to be sent to the channel.
type ImageEvent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/jpeg"
}

// Event is the consumer-facing stream event. Channels read these from the
// stream returned by Pool.Chat().
type Event struct {
	Text    string
	Image   *ImageEvent
	ToolUse *ToolUseEvent
	Store   ai.Message // if non-nil, Pool appends to session history
	Err     error
}

// MessageContent is the type for user messages passed through the runner pipeline.
// It is either string (text-only) or []ai.ContentBlock (multimodal, e.g. text + images).
type MessageContent = any

// Runner runs prompts against an AI backend.
// It is stateless — it receives full history each call and must
// reconstruct context from it.
type Runner interface {
	Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event
}

// NewRunnerFunc creates a new Runner instance for the given model ID.
// An empty model means use the default.
type NewRunnerFunc func(ctx context.Context, model string) (Runner, error)

// HandlerFunc is an adapter to allow the use of ordinary functions as Runners.
// If f is a function with the appropriate signature, HandlerFunc(f) is a Runner
// that calls f.
type HandlerFunc func(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event

// Chat calls f(ctx, history, message).
func (f HandlerFunc) Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event {
	return f(ctx, history, message)
}

// Stateful is an optional interface for runners that maintain their own
// context in-process (e.g., a long-running subprocess). When a runner is
// Stateful, Pool will not kill it after compaction — the runner keeps its
// live context and the compacted history is only persisted to disk for
// crash recovery.
type Stateful interface {
	Stateful() bool
}

// Aliver is an optional interface for runners that can report liveness.
type Aliver interface {
	Alive() bool
}

// ActivityTracker is an optional interface for runners that track last activity.
type ActivityTracker interface {
	LastActivity() time.Time
}

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
