package connections

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// Access is one connections use case bound to one trusted Authority. The
// user-facing OAuth capability is user-owned: OAuth bundles and flows are keyed
// by the acting user, so ownership is simply the captured userID — every
// operation is scoped to that user. A delegated AgentActor manages the same
// user's connections (an agent shares its user's bundles). There is no policy
// evaluation; the acting user is the boundary. Admin provider-config CRUD and the
// OAuth callback / token-refresh are separate trusted paths that call the raw
// Service methods directly and never open an Access.
type Access struct {
	svc    *Service
	userID string
}

// Access binds one connections use case to a trusted Authority. It rejects an
// invalid Authority (403) and one carrying no user (401) up front, so every
// method can assume a non-empty acting user.
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("oauth service is unavailable — try again later")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	userID := string(authority.Actor().UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	return &Access{svc: s, userID: userID}, nil
}

// Statuses returns the connection status for every registered provider for the
// acting user.
func (a *Access) Statuses(ctx context.Context) ([]ProviderStatus, error) {
	return a.svc.GetProviderStatuses(ctx, a.userID), nil
}

// StartFlow starts an OAuth flow for the acting user.
func (a *Access) StartFlow(ctx context.Context, provider string, origin ...string) (FlowStatus, error) {
	if len(origin) > 0 && origin[0] != "" {
		return a.svc.StartFlowWithOrigin(ctx, a.userID, provider, origin[0])
	}
	return a.svc.StartFlow(ctx, a.userID, provider)
}

// PollFlow polls an in-flight OAuth flow. Ownership is proven against the
// persisted flow owner; a foreign flow preserves the existing forbidden contract.
func (a *Access) PollFlow(ctx context.Context, provider, flowID string) (FlowStatus, bool, error) {
	if a.svc.flowStore == nil {
		return FlowStatus{}, false, authz.ErrNotFound
	}
	flow, ok := a.svc.flowStore.Get(flowID)
	if !ok {
		return FlowStatus{}, false, authz.ErrNotFound
	}
	if flow.UserID != a.userID {
		return FlowStatus{}, false, authz.ErrForbidden
	}
	if string(flow.Provider) != provider {
		return FlowStatus{}, false, authz.ErrNotFound
	}
	return a.svc.PollFlow(ctx, a.userID, provider, flowID)
}

// Disconnect removes the acting user's OAuth bundle for a provider.
func (a *Access) Disconnect(ctx context.Context, provider string) error {
	return a.svc.Disconnect(ctx, a.userID, provider)
}
