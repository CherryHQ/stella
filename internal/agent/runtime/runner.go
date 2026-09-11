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
	// Seq is the durable ctx_session_event sequence and DurableID its
	// run-scoped cursor ("runID:seq"), set only for events replayed from the
	// durable log — the SSE transport emits DurableID as the `id:` line so a
	// reconnect cursor unambiguously names which run it belongs to.
	Seq       int64
	DurableID string
}

// MessageContent is a user message: string (text) or []ai.ContentBlock (multimodal).
type MessageContent = any

// RunnerParams holds dependencies for creating a new Runner.
type RunnerParams struct {
	Model    string
	Thinking ai.ThinkingLevel
	Memory   any // memory.Provider — typed as any to avoid circular imports
	UserID   string
	GroupID  string // non-empty for group sessions; runtime uses this to isolate identity surfaces
	GuestID  string // durable guest identity; non-empty selects the no-capabilities runner
	// ForegroundHuman is derived from validated session metadata at runner build.
	// It is discovery-only and carries no Authority between turns.
	ForegroundHuman bool
	SessionID       string
	AgentID         string
	ProjectID       string
	HooksFn         func() []hooks.HookPlugin
	ExtraTools      []tools.Tool
	DelegateRunner  delegatetool.SessionRunner
	// PluginContext is an admission-captured context. Production builders set
	// PluginContextReady when a fresh identity was prepared so the runner and
	// cache compare and consume the same snapshot.
	PluginContext      PluginContext
	PluginContextReady bool
	// BuildOwner is registered in the runner cache before the factory starts
	// slow workspace/package work. It owns external cleanup until construction
	// completes and the returned Runner takes over the owner.
	BuildOwner *RunnerBuildOwner
}

// Runner executes prompts against an AI backend.
type Runner interface {
	Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event
	Alive() bool
	Busy() bool
	LastActivity() time.Time
	SystemPrompt() string
	PluginContext() PluginContext
	Close() error
}

// TurnPreparer refreshes resource-backed turn state on a cached runner. It is
// deliberately optional: runners without per-turn resources keep using their
// immutable admission context. Preparation happens after reservation, so a
// resource refresh never rebuilds the runner or changes its cached identity.
type TurnPreparer interface {
	PrepareTurn(context.Context, PluginContext) (context.Context, PluginContext, error)
}

// NewRunnerFunc creates a new Runner with the given params. On initialization
// failure it may return a non-nil Close-only runner so the caller can retry
// cleanup after a failed sandbox termination.
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
