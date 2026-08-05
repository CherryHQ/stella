package channel

import (
	"context"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// operationHandlerSurface is the full capability surface the managed channel
// handler (the Coordinator) exposes and that channel adapters type-assert for.
// The two-phase-drain wrapper embeds it so a wrapped handler still satisfies the
// BotRegistrar, RegisterGroupPublisher, Provisioner, UserRootResolver, and
// group-member-provisioner assertions the adapters make at construction and call
// time. Adding a capability that an adapter asserts requires adding it here.
type operationHandlerSurface interface {
	pkgchannel.Handler
	RegisterBotIdentity(platform, platformBotID, channelID string)
	UnregisterBotIdentity(platform, platformBotID, channelID string)
	RegisterGroupPublisher(channelID string, publisher GroupPublisher)
	UnregisterGroupPublisher(channelID string)
	ProvisionUser(ctx context.Context, req pkgchannel.ProvisionRequest) error
	ResolveUserRoot(ctx context.Context, msg pkgchannel.IncomingMessage) (string, error)
	EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
	RemovePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}

// Keep the production wrapper from silently degrading to pass-through if the
// Coordinator's capability surface changes.
var _ operationHandlerSurface = (*Coordinator)(nil)

// operationContextHandler decouples an accepted channel operation's lifetime
// from the poller that started it. A managed channel adapter derives its polling
// context from the context passed to Channel.Start and reuses it for the
// business-logic calls below, so cancelling the poll context (to stop ingress on
// a graceful drain) would also abort work already accepted. This wrapper
// substitutes a longer-lived operation context — one cancelled only at the
// runtime's final Stop — for every context-bearing handler capability.
//
// Context-free methods are promoted unchanged from the embedded surface,
// preserving the adapters' type assertions and behavior.
type operationContextHandler struct {
	operationHandlerSurface
	opCtx context.Context
}

// operationCallContext keeps call-scoped values and deadlines while taking
// cancellation from the longer-lived runtime operation context. This preserves
// Feishu's bounded per-turn deadline without letting poller shutdown abort work
// that was already accepted.
func operationCallContext(opCtx, callCtx context.Context) context.Context {
	parent := opCtx
	var deadlineCancel context.CancelFunc
	if deadline, ok := callCtx.Deadline(); ok {
		if parentDeadline, hasParentDeadline := parent.Deadline(); !hasParentDeadline || deadline.Before(parentDeadline) {
			// The timer is released by its deadline or when opCtx is cancelled at
			// final teardown. Keep its cancel function with the derived context rather
			// than propagating the poller's earlier cancellation.
			parent, deadlineCancel = context.WithDeadline(parent, deadline)
		}
	}
	return operationValueContext{Context: parent, values: callCtx, deadlineCancel: deadlineCancel}
}

type operationValueContext struct {
	context.Context
	values         context.Context
	deadlineCancel context.CancelFunc
}

func (c operationValueContext) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

// HandleIncoming runs on the operation context so an accepted chat survives the
// poll context being cancelled at drain.
func (h operationContextHandler) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	return h.operationHandlerSurface.HandleIncoming(operationCallContext(h.opCtx, ctx), msg, command, args)
}

// ListAgents runs on the operation context for the same reason as HandleIncoming.
func (h operationContextHandler) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return h.operationHandlerSurface.ListAgents(operationCallContext(h.opCtx, ctx), msg)
}

// SwitchAgent runs on the operation context for the same reason as HandleIncoming.
func (h operationContextHandler) SwitchAgent(ctx context.Context, msg pkgchannel.IncomingMessage, agentSlug string) error {
	return h.operationHandlerSurface.SwitchAgent(operationCallContext(h.opCtx, ctx), msg, agentSlug)
}

func (h operationContextHandler) ProvisionUser(ctx context.Context, req pkgchannel.ProvisionRequest) error {
	return h.operationHandlerSurface.ProvisionUser(operationCallContext(h.opCtx, ctx), req)
}

func (h operationContextHandler) ResolveUserRoot(ctx context.Context, msg pkgchannel.IncomingMessage) (string, error) {
	return h.operationHandlerSurface.ResolveUserRoot(operationCallContext(h.opCtx, ctx), msg)
}

func (h operationContextHandler) EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error {
	return h.operationHandlerSurface.EnsurePlatformGroupMember(operationCallContext(h.opCtx, ctx), platform, platformGroupID, channelID)
}

func (h operationContextHandler) RemovePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error {
	return h.operationHandlerSurface.RemovePlatformGroupMember(operationCallContext(h.opCtx, ctx), platform, platformGroupID, channelID)
}

// WrapOperationHandler returns a Handler whose context-bearing agent-facing
// methods run on opCtx instead of the caller's (poll) context. It is wired into
// managed channel runtimes so a two-phase drain can stop polling without
// cancelling operations already accepted.
//
// If h does not expose the full operation surface (e.g. a lightweight test
// handler), it is returned unchanged: wrapping would drop capabilities the
// adapter may still assert, and only the full managed handler needs the
// decoupling. A nil opCtx also returns h unchanged.
func WrapOperationHandler(h pkgchannel.Handler, opCtx context.Context) pkgchannel.Handler {
	if h == nil || opCtx == nil {
		return h
	}
	full, ok := h.(operationHandlerSurface)
	if !ok {
		return h
	}
	return operationContextHandler{operationHandlerSurface: full, opCtx: opCtx}
}
