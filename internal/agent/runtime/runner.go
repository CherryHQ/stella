// Package runtime executes agent conversations in already-resolved sessions.
//
// The only entry point for callers is Runtime.Chat. Callers must obtain a
// validated session.Info from session.Registry before calling Chat. Runtime
// never creates or repairs session metadata.
package runtime

import (
	"context"
	"fmt"
	"time"

	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/renderrefs"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolUseEvent describes a tool invocation in progress or completed.
type ToolUseEvent struct {
	ID         string
	Tool       string
	Status     string
	Input      string
	Arguments  map[string]any
	Detail     string
	Content    string
	References []renderrefs.Reference
}

// StepEvent marks the boundary of an agentic step.
type StepEvent struct {
	Kind string // "start" or "finish"
}

// ImageEvent carries a base64-encoded image.
type ImageEvent struct {
	Data     string
	MimeType string
}

// FileEvent carries a local file path.
type FileEvent struct {
	Path string
	Name string
}

// Event is the consumer-facing stream event.
type Event struct {
	Text       string
	Reasoning  string
	Image      *ImageEvent
	File       *FileEvent
	ToolUse    *ToolUseEvent
	References []renderrefs.Reference
	Step       *StepEvent
	Store      ai.Message // non-nil → append to session history
	Err        error
}

// MessageContent is a user message: string (text) or []ai.ContentBlock (multimodal).
type MessageContent = any

// RunnerParams holds dependencies for creating a new Runner.
type RunnerParams struct {
	Model       string
	Thinking    ai.ThinkingLevel
	Memory      any // memory.Provider — typed as any to avoid circular imports
	UserID      string
	GroupID     string // non-empty for group sessions; runtime uses this to isolate identity surfaces
	SessionID   string
	AgentID     string
	ProjectID   string
	SessionKind string
	// SessionChannel is a deny-only defense for automated session types. It
	// must never be used to grant PrivateHuman because one durable session can
	// be opened from more than one surface.
	SessionChannel string
	// PrivateHuman is a server-minted, per-turn capability. It is true only for
	// a private chat initiated by a human through the Web UI or a trusted chat
	// adapter; webhook and background callers leave it false.
	PrivateHuman   bool
	HooksFn        func() []hooks.HookPlugin
	ExtraTools     []tools.Tool
	DelegateRunner delegatetool.SessionRunner
}

// Runner executes prompts against an AI backend.
type Runner interface {
	Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event
	Alive() bool
	Busy() bool
	LastActivity() time.Time
	SystemPrompt() string
	Close() error
}

// NewRunnerFunc creates a new Runner with the given params.
type NewRunnerFunc func(ctx context.Context, params RunnerParams) (Runner, error)

// MessageText extracts and joins all text from a MessageContent.
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
