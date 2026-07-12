package connections

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// Access is one connections use case bound to exactly one Authorizer evaluation.
// The connections Service is the sole policy-enforcement point for the user-facing
// OAuth capability (ResourceConnection): transports and the agent tool pass a
// trusted authz.Authority and never a bare identity. A connection is user-owned —
// a delegated AgentActor manages the same user's connections (they are keyed by
// the delegating user's vault namespace), so the resource is authorized as the
// acting user's own. Admin provider-config CRUD and the OAuth callback / token
// refresh are separate trusted paths that do not open an Access.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	userID    string
	agentID   string
	authority authz.Authority
}

// Begin opens exactly one evaluation for one connections use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("connections authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("connections authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, userID: string(actor.UserID()), agentID: agentID, authority: authority}, nil
}

// Statuses returns the connection status for every registered provider for the
// acting user.
func (a *Access) Statuses(ctx context.Context) ([]ProviderStatus, error) {
	if a.userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	req, err := policy.ConnectionListRequest()
	if err != nil {
		return nil, authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return nil, err
	}
	return a.svc.GetProviderStatuses(ctx, a.userID), nil
}

// StartFlow starts an OAuth flow for the acting user.
func (a *Access) StartFlow(ctx context.Context, provider string, origin ...string) (FlowStatus, error) {
	if err := a.authorize(authz.ActionExecute, provider, a.userID, true); err != nil {
		return FlowStatus{}, err
	}
	if len(origin) > 0 && origin[0] != "" {
		return a.svc.StartFlowWithOrigin(ctx, a.userID, provider, origin[0])
	}
	return a.svc.StartFlow(ctx, a.userID, provider)
}

// PollFlow polls an in-flight OAuth flow, hiding a flow owned by another user.
func (a *Access) PollFlow(ctx context.Context, provider, flowID string) (FlowStatus, bool, error) {
	if a.userID == "" {
		return FlowStatus{}, false, authz.ErrUnauthenticated
	}
	if a.svc.flowStore == nil {
		return FlowStatus{}, false, authz.ErrNotFound
	}
	flow, ok := a.svc.flowStore.Get(flowID)
	if !ok {
		return FlowStatus{}, false, authz.ErrNotFound
	}
	// is_owner is derived from the durable flow owner; a foreign flow is denied.
	if err := a.authorize(authz.ActionRead, provider, flow.UserID, flow.UserID == a.userID); err != nil {
		return FlowStatus{}, false, err
	}
	return a.svc.PollFlow(ctx, a.userID, provider, flowID)
}

// Disconnect removes the acting user's OAuth bundle for a provider.
func (a *Access) Disconnect(ctx context.Context, provider string) error {
	if err := a.authorize(authz.ActionDelete, provider, a.userID, true); err != nil {
		return err
	}
	return a.svc.Disconnect(ctx, a.userID, provider)
}

// authorize decides one connection action for the acting user under this
// Access's single revision. A denial is an authenticated 403 (ErrForbidden),
// preserving the pre-cutover contract.
func (a *Access) authorize(action authz.Action, provider, owner string, isOwner bool) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	facts := policy.ConnectionFacts{Owner: owner, Agent: a.agentID, Type: provider, IsOwner: isOwner}
	req, err := policy.ConnectionRequest(action, owner, owner, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decideReq(req)
}

func (a *Access) decideReq(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("connections decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrForbidden
	}
	return nil
}
