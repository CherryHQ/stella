package channel

import (
	"context"

	"github.com/CherryHQ/stella/internal/orgctx"
)

// HandlerWithOrgID returns a Handler that injects orgID into the context of
// every call before delegating to inner. Channel plugins receive callbacks
// from their SDKs (larkws, telegram poller, etc.) on a context that does not
// carry any HTTP middleware state. Wrapping the handler at runtime construction
// time ensures downstream consumers (coordinator, agent pool) see the correct
// org without each channel implementation having to know about org plumbing.
//
// The returned wrapper preserves inner's optional interfaces (Provisioner,
// UserRootResolver) via type-assertion fanout so channel plugins that branch on
// `b.handler.(channel.UserRootResolver)` keep working.
//
// orgID may be empty if the caller cannot resolve it; in that case the wrapper
// is a passthrough (no value injected). Callers that require non-empty orgID
// should validate before calling.
func HandlerWithOrgID(orgID string, inner Handler) Handler {
	if orgID == "" {
		return inner
	}
	base := orgScopedHandler{orgID: orgID, inner: inner}
	_, hasResolver := inner.(UserRootResolver)
	_, hasProvisioner := inner.(Provisioner)
	switch {
	case hasResolver && hasProvisioner:
		return orgScopedResolverProvisioner{orgScopedHandler: base}
	case hasResolver:
		return orgScopedResolver{orgScopedHandler: base}
	case hasProvisioner:
		return orgScopedProvisioner{orgScopedHandler: base}
	default:
		return base
	}
}

type orgScopedResolver struct{ orgScopedHandler }

func (h orgScopedResolver) ResolveUserRoot(ctx context.Context, msg IncomingMessage) (string, error) {
	return h.inner.(UserRootResolver).ResolveUserRoot(orgctx.WithOrgID(ctx, h.orgID), msg)
}

type orgScopedProvisioner struct{ orgScopedHandler }

func (h orgScopedProvisioner) ProvisionUser(ctx context.Context, req ProvisionRequest) error {
	return h.inner.(Provisioner).ProvisionUser(orgctx.WithOrgID(ctx, h.orgID), req)
}

type orgScopedResolverProvisioner struct{ orgScopedHandler }

func (h orgScopedResolverProvisioner) ResolveUserRoot(ctx context.Context, msg IncomingMessage) (string, error) {
	return h.inner.(UserRootResolver).ResolveUserRoot(orgctx.WithOrgID(ctx, h.orgID), msg)
}

func (h orgScopedResolverProvisioner) ProvisionUser(ctx context.Context, req ProvisionRequest) error {
	return h.inner.(Provisioner).ProvisionUser(orgctx.WithOrgID(ctx, h.orgID), req)
}

type orgScopedHandler struct {
	orgID string
	inner Handler
}

func (h orgScopedHandler) HandleIncoming(ctx context.Context, msg IncomingMessage, command, args string) (string, bool, *ChatStream, error) {
	return h.inner.HandleIncoming(orgctx.WithOrgID(ctx, h.orgID), msg, command, args)
}

func (h orgScopedHandler) ListModels() []ModelOption { return h.inner.ListModels() }

func (h orgScopedHandler) SwitchModel(provider, model string) error {
	return h.inner.SwitchModel(provider, model)
}

func (h orgScopedHandler) ListAgents(ctx context.Context, msg IncomingMessage) ([]AgentInfo, string, error) {
	return h.inner.ListAgents(orgctx.WithOrgID(ctx, h.orgID), msg)
}

func (h orgScopedHandler) SwitchAgent(ctx context.Context, msg IncomingMessage, agentSlug string) error {
	return h.inner.SwitchAgent(orgctx.WithOrgID(ctx, h.orgID), msg, agentSlug)
}
