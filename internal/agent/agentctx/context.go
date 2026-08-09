package agentctx

import (
	"context"
	"fmt"
	"slices"
)

// MaxCallDepth bounds synchronous Session/delegate nesting. Raise it only when
// real workflows need deeper collaboration and root turn budgets still bound
// the resulting fan-out safely.
const MaxCallDepth = 4

type (
	systemOverrideKey struct{}
	channelKey        struct{}
	excludedToolsKey  struct{}
	chatBindingKey    struct{}
	turnKey           struct{}
	sessionCallKey    struct{}
)

// SessionCall is the live synchronous Session-call chain. It is deliberately
// context-only: the caller and every child die together, so persistence would
// create replay risk rather than durability.
type SessionCall struct {
	Depth    int
	Ancestry []string
}

// EnterSessionCall increments nesting and optionally appends a known target.
// sourceSessionID seeds a top-level chain; inherited chains already contain it.
func EnterSessionCall(ctx context.Context, sourceSessionID, targetSessionID string) (context.Context, error) {
	call, _ := SessionCallFromContext(ctx)
	if len(call.Ancestry) == 0 && sourceSessionID != "" {
		call.Ancestry = append(call.Ancestry, sourceSessionID)
	}
	if call.Depth >= MaxCallDepth {
		return ctx, fmt.Errorf("session call depth limit reached (maximum %d)", MaxCallDepth)
	}
	call.Depth++
	if targetSessionID != "" {
		if slices.Contains(call.Ancestry, targetSessionID) {
			return ctx, fmt.Errorf("session call cycle detected: target session %s is already in the active ancestry", targetSessionID)
		}
		call.Ancestry = append(call.Ancestry, targetSessionID)
	}
	return withSessionCall(ctx, call), nil
}

// BindSessionCallTarget adds a target resolved after call entry (session.create).
// Binding the current tail is idempotent for known-target send paths.
func BindSessionCallTarget(ctx context.Context, targetSessionID string) (context.Context, error) {
	if targetSessionID == "" {
		return ctx, nil
	}
	call, ok := SessionCallFromContext(ctx)
	if !ok {
		return ctx, nil
	}
	if len(call.Ancestry) > 0 && call.Ancestry[len(call.Ancestry)-1] == targetSessionID {
		return ctx, nil
	}
	if slices.Contains(call.Ancestry, targetSessionID) {
		return ctx, fmt.Errorf("session call cycle detected: target session %s is already in the active ancestry", targetSessionID)
	}
	call.Ancestry = append(call.Ancestry, targetSessionID)
	return withSessionCall(ctx, call), nil
}

func withSessionCall(ctx context.Context, call SessionCall) context.Context {
	call.Ancestry = append([]string(nil), call.Ancestry...)
	return context.WithValue(ctx, sessionCallKey{}, call)
}

// SessionCallFromContext returns a defensive copy of the active call chain.
func SessionCallFromContext(ctx context.Context) (SessionCall, bool) {
	if ctx == nil {
		return SessionCall{}, false
	}
	call, ok := ctx.Value(sessionCallKey{}).(SessionCall)
	if !ok {
		return SessionCall{}, false
	}
	call.Ancestry = append([]string(nil), call.Ancestry...)
	return call, true
}

// ChatBinding describes the durable channel binding a chat turn entered
// through. Only the chat channel adapters attach it, so its absence is the
// signal that a turn is not channel-backed: Web/API sends, webhooks, and
// scheduler/task/delegate runs all leave it unset and therefore fail closed in
// consumers that require a durable chat.
//
// It exists because a session row cannot answer the question. The Web UI can
// open the very same main session a DM is pinned to (`POST /sessions` with
// kind=main), and `ctx_conversation.channel` records whichever surface created
// the row first, so neither the session id nor its channel distinguishes a
// Telegram turn from a browser tab on the same session. The entry adapter does
// know, and this is how it says so.
type ChatBinding struct {
	// Main marks a DM pinned to its user's singleton main session. Every other
	// chat (group, private channel) resolves through the channel binding below.
	Main bool
	// Channel is the durable session channel of a non-main chat binding.
	Channel string
	// SessionKey is the chat's derived key, kept for legacy key-as-id lookups.
	SessionKey string
}

// WithChatBinding returns a child context carrying the durable chat binding
// this turn entered through.
func WithChatBinding(ctx context.Context, binding ChatBinding) context.Context {
	return context.WithValue(ctx, chatBindingKey{}, binding)
}

// clearedChatBinding shadows an inherited ChatBinding. The key must stay
// occupied — deleting a context value is impossible — so it holds a value of a
// type no reader can assert to, which makes ChatBindingFromContext answer false
// exactly as it does for a context that never carried a binding.
type clearedChatBinding struct{}

// WithoutChatBinding returns a child context with no chat binding, whatever the
// parent carried.
//
// A delegate (or any other nested run) inherits its parent's context, and the
// parent may be a Telegram or group turn. The binding is the authority to rotate
// and compact THAT chat's session, so inheriting it would let a delegate — whose
// own turn nobody is watching and whose task text can come from a tool result —
// reset the conversation that spawned it.
func WithoutChatBinding(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	if _, ok := ChatBindingFromContext(ctx); !ok {
		return ctx
	}
	return context.WithValue(ctx, chatBindingKey{}, clearedChatBinding{})
}

// ChatBindingFromContext returns the durable chat binding of the current turn.
// The bool is false for any turn that did not enter through a chat channel.
func ChatBindingFromContext(ctx context.Context) (ChatBinding, bool) {
	if ctx == nil {
		return ChatBinding{}, false
	}
	binding, ok := ctx.Value(chatBindingKey{}).(ChatBinding)
	return binding, ok
}

// WithTurnID returns a child context carrying an identifier unique to one chat
// turn. The runtime mints it once per Chat call; consumers use it only to tell
// two turns apart, never to order them.
func WithTurnID(ctx context.Context, turnID string) context.Context {
	if turnID == "" {
		return ctx
	}
	return context.WithValue(ctx, turnKey{}, turnID)
}

// TurnIDFromContext returns the current turn's identifier, or "" outside a turn.
func TurnIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	turnID, _ := ctx.Value(turnKey{}).(string)
	return turnID
}

// WithSystemOverride returns a child context that carries a per-run system prompt override.
func WithSystemOverride(ctx context.Context, system string) context.Context {
	if system == "" {
		return ctx
	}
	return context.WithValue(ctx, systemOverrideKey{}, system)
}

// SystemOverrideFromContext returns the per-run system prompt override when present.
func SystemOverrideFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	system, ok := ctx.Value(systemOverrideKey{}).(string)
	return system, ok && system != ""
}

// WithChannel returns a child context that carries the current chat channel.
func WithChannel(ctx context.Context, channel string) context.Context {
	if channel == "" {
		return ctx
	}
	return context.WithValue(ctx, channelKey{}, channel)
}

// ChannelFromContext returns the current chat channel when present.
func ChannelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	channel, ok := ctx.Value(channelKey{}).(string)
	return channel, ok && channel != ""
}

// WithExcludedTools returns a child context that hides the named tools for a single run.
func WithExcludedTools(ctx context.Context, names ...string) context.Context {
	filtered := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		filtered = append(filtered, name)
	}
	if len(filtered) == 0 {
		return ctx
	}
	return context.WithValue(ctx, excludedToolsKey{}, filtered)
}

// ExcludedToolsFromContext returns the per-run excluded tool names when present.
func ExcludedToolsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	names, _ := ctx.Value(excludedToolsKey{}).([]string)
	if len(names) == 0 {
		return nil
	}
	out := make([]string, len(names))
	copy(out, names)
	return out
}
