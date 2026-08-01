package agent

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

// ToolFunc executes one tool invocation, returning its result as content blocks
// (text and/or images).
type ToolFunc func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error)

// ToolResultTransform canonicalizes a final tool result after lifecycle
// mutations and before it is observed, appended to the loop, or persisted.
type ToolResultTransform func(context.Context, ai.ToolResultMessage) (ai.ToolResultMessage, error)

// ToolSet maps tool names to handlers.
type ToolSet map[string]ToolFunc

// loopConfig configures the agent loop behavior.
type loopConfig struct {
	Stream          providers.StreamFunc
	Model           ai.Model
	StreamOptions   ai.StreamOptions
	Tools           ToolSet
	ToolDefinitions []ai.ToolDefinition
	System          string
	Interrupt       <-chan struct{}
	Hooks           *hooks.HookSet
	HookMeta        hooks.HookMeta
	ToolLifecycle   *ToolLifecycle
	ToolTransform   ToolResultTransform
	// ImageText renders legacy inline images as text. New canonical references
	// use MediaLoader and their stored baseline instead.
	ImageText          ImageTextFunc
	MediaLoader        MediaLoader
	ProjectionObserver ProjectionObserver
	ImageProjection    bool
	// TurnNotify is called at the start of each turn. If it returns a non-nil
	// string, that text is injected as a UserMessage before the model call.
	// Intended for progress nudges at milestone turns (e.g. 50, 80, 100).
	// The injected messages are ephemeral — they exist only in the in-memory
	// history slice and are not persisted to the session store. The model's
	// responses to nudges ARE persisted, so they should read coherently standalone.
	TurnNotify func(turn int, elapsed time.Duration) *string
}
