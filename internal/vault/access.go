package vault

import (
	"context"
	"errors"
	"fmt"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// Access is one vault use case bound to exactly one Authorizer evaluation. The
// vault Service is the sole policy-enforcement point for ResourceVault: the HTTP
// handlers and the agent tool pass a trusted authz.Authority and never a bare
// identity or an IsAdmin bool. The four durable scopes map to policy this way:
//   - user / user_agent are user-owned (is_owner is derived from the entry's
//     owner column; an agent-scoped actor is confined to its own user_agent
//     bucket, and every agent-scoped op folds an agent-read gate);
//   - system / system_agent are admin-managed and reachable only through
//     admin-full-access (there is no owner built-in for them).
//
// Trusted internal callers (MCP, connections/OAuth, email, channel config,
// sandbox env loader, key provisioning) keep using the raw Service methods; they
// are host-side credential plumbing, not user requests, and never open an Access.
type Access struct {
	svc         *Service
	eval        authz.Evaluation
	authority   authz.Authority
	userID      string
	agentID     string
	agentScoped bool
}

// Begin opens exactly one evaluation for one vault use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("vault authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("vault authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	agentScoped := false
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
		agentScoped = true
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), agentID: agentID, agentScoped: agentScoped}, nil
}

// ListScoped lists entry metadata for one scope, or (scope == "") the caller's
// user and user_agent buckets aggregated, matching the pre-cutover tool default.
func (a *Access) ListScoped(ctx context.Context, scope, agentID string) ([]EntryMeta, error) {
	if scope == "" {
		userEntries, err := a.listOne(ctx, ScopeUser, "")
		if err != nil {
			return nil, err
		}
		// Only an agent-scoped caller has an implicit user_agent bucket to aggregate.
		if a.agentID == "" {
			return userEntries, nil
		}
		agentEntries, err := a.listOne(ctx, ScopeUserAgent, a.agentID)
		if err != nil {
			return nil, err
		}
		return append(userEntries, agentEntries...), nil
	}
	return a.listOne(ctx, scope, agentID)
}

func (a *Access) listOne(ctx context.Context, scope, agentID string) ([]EntryMeta, error) {
	userID, resolvedAgent, err := a.authorizeScoped(ctx, authz.ActionList, scope, agentID)
	if err != nil {
		return nil, err
	}
	if isSystemScope(scope) {
		return a.svc.ListSystemScoped(ctx, scope, resolvedAgent)
	}
	return a.svc.listScoped(ctx, scope, userID, resolvedAgent)
}

// GetScoped decrypts and returns one entry's plaintext value.
func (a *Access) GetScoped(ctx context.Context, scope, agentID, name string) (string, error) {
	userID, resolvedAgent, err := a.authorizeScoped(ctx, authz.ActionRead, scope, agentID)
	if err != nil {
		return "", err
	}
	return a.svc.GetScoped(ctx, scope, userID, resolvedAgent, name)
}

// GetScopedMeta returns non-sensitive metadata for one entry.
func (a *Access) GetScopedMeta(ctx context.Context, scope, agentID, name string) (EntryMeta, error) {
	userID, resolvedAgent, err := a.authorizeScoped(ctx, authz.ActionRead, scope, agentID)
	if err != nil {
		return EntryMeta{}, err
	}
	return a.svc.GetScopedMeta(ctx, scope, userID, resolvedAgent, name)
}

// SetScoped stores an entry after a write decision.
func (a *Access) SetScoped(ctx context.Context, scope, agentID, name, value string, opts SetOptions) error {
	userID, resolvedAgent, err := a.authorizeScoped(ctx, authz.ActionWrite, scope, agentID)
	if err != nil {
		return err
	}
	if isSystemScope(scope) {
		return a.svc.SetSystemScopedWithOptions(ctx, scope, resolvedAgent, name, value, opts)
	}
	return a.svc.SetScopedWithOptions(ctx, scope, userID, resolvedAgent, name, value, opts)
}

// DeleteScoped removes an entry after a delete decision.
func (a *Access) DeleteScoped(ctx context.Context, scope, agentID, name string) error {
	userID, resolvedAgent, err := a.authorizeScoped(ctx, authz.ActionDelete, scope, agentID)
	if err != nil {
		return err
	}
	if isSystemScope(scope) {
		return a.svc.DeleteSystemScoped(ctx, scope, resolvedAgent, name)
	}
	return a.svc.DeleteScoped(ctx, scope, userID, resolvedAgent, name)
}

// authorizeScoped resolves an entry's owner/agent columns for a scope, decides the
// action against ResourceVault, and folds the agent-read gate for agent-scoped
// buckets — all under this Access's single revision. It returns the resolved
// (userID, agentID) columns for the durable call. A policy denial is 403
// (ErrForbidden); a system scope is reachable only via admin-full-access.
func (a *Access) authorizeScoped(ctx context.Context, action authz.Action, scope, requestedAgentID string) (string, string, error) {
	var entryUserID, entryAgentID string
	switch scope {
	case ScopeUser:
		if a.userID == "" {
			return "", "", authz.ErrUnauthenticated
		}
		entryUserID = a.userID
	case ScopeUserAgent:
		if a.userID == "" {
			return "", "", authz.ErrUnauthenticated
		}
		// An agent-scoped actor defaults to (and is confined to) its own bucket.
		if requestedAgentID == "" && a.agentScoped {
			requestedAgentID = a.agentID
		}
		if a.agentScoped && requestedAgentID != a.agentID {
			return "", "", authz.ErrForbidden
		}
		entryUserID = a.userID
		entryAgentID = requestedAgentID
	case ScopeSystem:
		// admin-only, decided by policy below.
	case ScopeSystemAgent:
		// admin-only; the agent column is validated after the policy decision so a
		// non-admin is denied (access-denied), not nagged for a missing agent_id.
		entryAgentID = requestedAgentID
	default:
		return "", "", fmt.Errorf("vault: invalid scope %q", scope)
	}
	// Decide the policy FIRST so a caller with no grant for a scope is denied
	// before any structural validation of that scope's columns.
	isOwner := !isSystemScope(scope) && a.userID != "" && entryUserID == a.userID
	facts := policy.VaultFacts{Scope: scope, Owner: entryUserID, Agent: entryAgentID, IsOwner: isOwner}
	req, err := policy.VaultRequest(action, entryUserID, entryUserID, facts)
	if err != nil {
		return "", "", authz.ErrForbidden
	}
	if err := a.decideReq(req); err != nil {
		return "", "", err
	}
	// The authorized caller must still name a valid bucket.
	if (scope == ScopeUserAgent || scope == ScopeSystemAgent) && entryAgentID == "" {
		return "", "", fmt.Errorf("vault: agent_id is required for %s scope", scope)
	}
	if err := validateScope(scope, entryUserID, entryAgentID); err != nil {
		return "", "", err
	}
	// Every agent-scoped bucket additionally requires Agent-domain read access.
	if entryAgentID != "" {
		if err := a.authorizeAgent(ctx, entryAgentID); err != nil {
			return "", "", err
		}
	}
	return entryUserID, entryAgentID, nil
}

func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	if a.svc.agents == nil {
		return authz.ErrForbidden
	}
	err := a.svc.agents.Authorize(ctx, a.authority, agentID, authz.ActionRead)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound):
		return authz.ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return authz.ErrForbidden
	default:
		return err
	}
}

func (a *Access) decideReq(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("vault decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrForbidden
	}
	return nil
}
