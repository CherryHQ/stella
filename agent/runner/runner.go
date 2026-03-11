package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/vaayne/anna/ai"
)

// RPCCommand represents a command in the runner protocol.
type RPCCommand struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// RPCEvent type constants.
const (
	RPCEventUserMessage   = "user_message"
	RPCEventMessageUpdate = "message_update"
	RPCEventToolCall      = "tool_call"
	RPCEventToolResult    = "tool_result"
	RPCEventToolStart     = "tool_start"
	RPCEventToolEnd       = "tool_end"
	RPCEventAgentEnd      = "agent_end"
)

// Content block kind constants used in ContentBlockJSON serialization.
const (
	BlockKindText  = "text"
	BlockKindImage = "image"
)

// RPCEvent represents an event in the runner protocol.
// Pool stores these verbatim as the session history.
type RPCEvent struct {
	Type                  string          `json:"type"`
	AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent,omitempty"`
	ID                    string          `json:"id,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
	Error                 string          `json:"error,omitempty"`
	Tool                  string          `json:"tool,omitempty"`
	Summary               string          `json:"summary,omitempty"`
	Content               json.RawMessage `json:"content,omitempty"` // multimodal content blocks (images + text)
}

// AssistantMessageEvent represents the inner event for text deltas.
type AssistantMessageEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

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
	Store   *RPCEvent // if set, Pool appends to session history
	Err     error
}

// MessageContent is the type for user messages passed through the runner pipeline.
// It is either string (text-only) or []ai.ContentBlock (multimodal, e.g. text + images).
type MessageContent = any

// Runner runs prompts against an AI backend.
// It is stateless — it receives full history each call and must
// reconstruct context from it.
type Runner interface {
	Chat(ctx context.Context, history []RPCEvent, message MessageContent) <-chan Event
}

// NewRunnerFunc creates a new Runner instance for the given model ID.
// An empty model means use the default.
type NewRunnerFunc func(ctx context.Context, model string) (Runner, error)

// HandlerFunc is an adapter to allow the use of ordinary functions as Runners.
// If f is a function with the appropriate signature, HandlerFunc(f) is a Runner
// that calls f.
type HandlerFunc func(ctx context.Context, history []RPCEvent, message MessageContent) <-chan Event

// Chat calls f(ctx, history, message).
func (f HandlerFunc) Chat(ctx context.Context, history []RPCEvent, message MessageContent) <-chan Event {
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

// ContentBlockJSON is the JSON-serializable representation of a content block.
type ContentBlockJSON struct {
	Kind     string `json:"kind"`                // "text" or "image"
	Text     string `json:"text,omitempty"`      // for text blocks
	Data     string `json:"data,omitempty"`      // base64 for image blocks
	MimeType string `json:"mime_type,omitempty"` // for image blocks
}

// UserMessageToRPCEvent creates an RPCEvent for a user message.
func UserMessageToRPCEvent(message MessageContent) RPCEvent {
	evt := RPCEvent{Type: RPCEventUserMessage}
	switch m := message.(type) {
	case string:
		evt.Summary = m
	case []ai.ContentBlock:
		var blocks []ContentBlockJSON
		for _, b := range m {
			switch b := b.(type) {
			case ai.TextContent:
				blocks = append(blocks, ContentBlockJSON{Kind: BlockKindText, Text: b.Text})
				if evt.Summary == "" {
					evt.Summary = b.Text
				}
			case ai.ImageContent:
				blocks = append(blocks, ContentBlockJSON{Kind: BlockKindImage, Data: b.Data, MimeType: b.MimeType})
			}
		}
		if data, err := json.Marshal(blocks); err != nil {
			slog.Warn("failed to marshal multimodal content", "error", err)
		} else {
			evt.Content = data
		}
	default:
		evt.Summary = fmt.Sprintf("%v", message)
	}
	return evt
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

// TextDeltaToRPCEvent converts a text delta string to an RPCEvent for storage.
func TextDeltaToRPCEvent(text string) RPCEvent {
	inner, _ := json.Marshal(AssistantMessageEvent{Type: "text_delta", Delta: text})
	return RPCEvent{
		Type:                  RPCEventMessageUpdate,
		AssistantMessageEvent: inner,
	}
}

// AssistantMessageToRPCEvent converts a complete assistant message to an RPCEvent.
func AssistantMessageToRPCEvent(text string) RPCEvent {
	return RPCEvent{
		Type:    RPCEventMessageUpdate,
		Summary: text,
	}
}

// ToolCallToRPCEvent converts a tool call to an RPCEvent for history storage.
func ToolCallToRPCEvent(call ai.ToolCall) RPCEvent {
	argsJSON, _ := json.Marshal(call.Arguments)
	return RPCEvent{
		Type:   RPCEventToolCall,
		ID:     call.ID,
		Tool:   call.Name,
		Result: argsJSON,
	}
}

// ToolResultToRPCEvent converts a tool result to an RPCEvent for history storage.
func ToolResultToRPCEvent(result ai.ToolResultMessage) RPCEvent {
	text := ai.FlattenText(result.Content)
	contentJSON, _ := json.Marshal(text)
	evt := RPCEvent{
		Type:   RPCEventToolResult,
		ID:     result.ToolCallID,
		Tool:   result.ToolName,
		Result: contentJSON,
	}
	if result.IsError {
		evt.Error = text
	}
	return evt
}
