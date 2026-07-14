package skillaccess

import (
	"context"
	"errors"
	"fmt"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skills"
)

// Access captures one validated authority for a Skill use case. Skill owns the
// direct rules for DB-backed skill rows; every decision reads only immutable
// authority plus the durable row/scope facts, so there is no revision to
// re-read between use cases.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
	// scopeAgentID is the executor confinement: empty for a plain user actor, the
	// bound agent for a delegated AgentActor (which may only touch its own).
	scopeAgentID string
}

// Begin captures validated authority for one Skill use case.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if s.agents == nil {
		return nil, fmt.Errorf("%w: agent access not configured", ErrUnavailable)
	}
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, authority: authority, userID: string(actor.UserID()), scopeAgentID: agentID}, nil
}

// AuthorizeManageScope authorizes managing a whole scope bucket — create,
// install, upload, or the scope-keyed management list — and returns the owner
// columns a row of that scope carries. user/user_agent require the acting user to
// be the owner (always true here); system/system_agent require the admin
// superuser. When agentID is non-empty the caller's read access to that agent is
// folded in (the former requireAgentAccess gate).
//
// The management-list reuses this create-authority decision deliberately: the
// pre-cutover resolveSkillManageScope was the single gate for both listing and
// creating a scope, so denying a scope also hides its list.
func (a *Access) AuthorizeManageScope(ctx context.Context, scope, agentID string) (uid, aid string, err error) {
	if a.userID == "" {
		return "", "", ErrForbidden
	}
	// A delegated agent is confined to its own agent; it may not manage another's.
	if a.scopeAgentID != "" && agentID != "" && a.scopeAgentID != agentID {
		return "", "", ErrForbidden
	}
	uid, aid, ok := scopeOwner(scope, a.userID, agentID)
	if !ok {
		return "", "", ErrInvalidScope
	}
	if err := a.decide(authz.ActionCreate, scope, uid != "" && uid == a.userID); err != nil {
		return "", "", err
	}
	if agentID != "" {
		if err := a.authorizeAgent(ctx, agentID); err != nil {
			return "", "", err
		}
	}
	return uid, aid, nil
}

// AuthorizeManage authorizes an action on one already-loaded skill row. Reading,
// writing, or deleting a user/user_agent skill requires the acting user to own
// it (else the denial is opaque, ErrNotFound); any action on an admin-managed
// system/system_agent skill requires the admin superuser (ErrForbidden). For
// agent-bound scopes the caller's read access to the skill's agent is folded in.
func (a *Access) AuthorizeManage(ctx context.Context, sk skills.Skill, action authz.Action) error {
	if a.userID == "" {
		return ErrForbidden
	}
	// A delegated agent never touches another agent's bound skills.
	if a.scopeAgentID != "" && sk.AgentID != "" && a.scopeAgentID != sk.AgentID {
		return ErrNotFound
	}
	// Managing an admin scope requires the admin superuser regardless of the
	// requested verb: reading a system skill through the management API is itself
	// an admin operation (a user reads shared system skills through the agent
	// skills view, not this by-id management path).
	decided := action
	if adminScope(sk.Scope) {
		decided = authz.ActionManage
	}
	if err := a.decide(decided, sk.Scope, a.userID != "" && a.userID == sk.UserID); err != nil {
		return err
	}
	if agentBound(sk.Scope) && sk.AgentID != "" {
		if err := a.authorizeAgent(ctx, sk.AgentID); err != nil {
			return err
		}
	}
	return nil
}

// AuthorizeRead authorizes reading one DB-backed skill row through the resolve/
// list surface (agent skills tool, agent HTTP list/get/file). Unlike
// AuthorizeManage it uses ActionRead without escalating system scopes to Manage,
// so a user or delegated agent may read shared system/system_agent procedures
// while a since-revoked agent grant still hides the row. For agent-bound scopes
// the caller's read access to the skill's agent is folded in. A user-scope denial
// is opaque (ErrNotFound); a system-scope denial is ErrForbidden — the caller
// decides whether to skip (list) or 404 (single load).
func (a *Access) AuthorizeRead(ctx context.Context, sk skills.Skill) error {
	// No a.userID != "" guard here: Begin already validated the authority, and a
	// GroupAgentActor (a group turn) legitimately has no user. Its read visibility
	// is decided by the direct rule (system/system_agent only) plus the folded
	// agent gate. A delegated agent never reads another agent's bound skills.
	if a.scopeAgentID != "" && sk.AgentID != "" && a.scopeAgentID != sk.AgentID {
		return ErrNotFound
	}
	if err := a.decide(authz.ActionRead, sk.Scope, a.userID != "" && a.userID == sk.UserID); err != nil {
		return err
	}
	if agentBound(sk.Scope) && sk.AgentID != "" {
		if err := a.authorizeAgent(ctx, sk.AgentID); err != nil {
			return err
		}
	}
	return nil
}

// AuthorizeManageByID loads a skill row by id and authorizes an action on it. A
// missing row is opaque (ErrNotFound). It returns the loaded row so the caller
// can perform the store mutation it just authorized.
func (a *Access) AuthorizeManageByID(ctx context.Context, id string, action authz.Action) (skills.Skill, error) {
	sk, err := a.load(ctx, id)
	if err != nil {
		return skills.Skill{}, err
	}
	if err := a.AuthorizeManage(ctx, sk, action); err != nil {
		return skills.Skill{}, err
	}
	return sk, nil
}

// AuthorizeWorkerWrite authorizes a durable worker's write to a reflect-owned
// user_agent skill under a freshly reconstructed WorkerAgentAuthority(userID,
// agentID). create=true authorizes minting a new row (scope create); otherwise it
// authorizes writing the existing skillID. It is the single reauthorization point
// for reflect's staged skill reconciliation and usage curation, so a since-revoked
// agent grant stops the write.
func (s *Service) AuthorizeWorkerWrite(ctx context.Context, userID, agentID, skillID string, create bool) error {
	authority, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		return ErrForbidden
	}
	acc, err := s.Begin(ctx, authority)
	if err != nil {
		return err
	}
	if create {
		_, _, err := acc.AuthorizeManageScope(ctx, ScopeUserAgent, agentID)
		return err
	}
	// Load the real row and authorize against its durable owner/agent facts; the
	// caller-supplied userID/agentID only reconstruct the acting authority, never
	// the resource's ownership, so a worker cannot claim a foreign owner.
	_, err = acc.AuthorizeManageByID(ctx, skillID, authz.ActionWrite)
	return err
}

// AuthorizeList authorizes the collection-level skill list. Per-scope and per-row
// visibility is decided separately in the same use case (AuthorizeManageScope /
// AuthorizeManage).
func (a *Access) AuthorizeList() error {
	if a.userID == "" {
		return ErrForbidden
	}
	if !a.allow(authz.ActionList, "", false) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeAgent folds the route agent's read gate into this use case. The
// agent-scoped skill endpoints reach a skill through /api/agents/{id}/skills, so
// the path agent {id} is gated alongside the skill decision, for every scope —
// including user and system DB skills whose own row is not agent-bound. This
// replaces the preliminary requireAgentAccess split evaluation.
func (a *Access) AuthorizeAgent(ctx context.Context, agentID string) error {
	return a.authorizeAgent(ctx, agentID)
}

// load resolves a skill row by id, opaque on a miss. The store has no Get-by-id,
// so it scans ListAll — correct at current volumes, matching the transports.
func (a *Access) load(ctx context.Context, id string) (skills.Skill, error) {
	rows, err := a.svc.store.ListAll(ctx)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("%w: list skills: %w", ErrUnavailable, err)
	}
	for i := range rows {
		if rows[i].ID == id {
			return rows[i], nil
		}
	}
	return skills.Skill{}, ErrNotFound
}

// decide applies Skill's direct rule for one scope/owner and maps a denial to the
// scope's visibility: admin-managed scopes are Forbidden (403), user-owned scopes
// are opaque (404).
func (a *Access) decide(action authz.Action, scope string, isOwner bool) error {
	if a.allow(action, scope, isOwner) {
		return nil
	}
	if adminScope(scope) {
		return ErrForbidden
	}
	return ErrNotFound
}

// allow is Skill's static rule table. isOwner reports whether the acting user
// owns the row (derived at the call site from immutable authority plus the
// durable owner column). scope is empty for the collection-level list.
func (a *Access) allow(action authz.Action, scope string, isOwner bool) bool {
	if !action.Valid() {
		return false
	}
	if a.authority.IsAdmin() {
		return true
	}
	switch a.authority.Kind() {
	case authz.ActorUser:
		if !a.authority.HasRole(authz.RoleUser) {
			return false
		}
		return actorSkillAllowed(action, scope, isOwner)
	case authz.ActorAgent:
		// A delegated agent shares the delegating user's skill rules; its executor
		// confinement and folded agent gate are enforced by the callers.
		return actorSkillAllowed(action, scope, isOwner)
	case authz.ActorGroupAgent:
		// A group turn has no user: it reads only the shared, non-personal
		// system/system_agent reference procedures, and only with the explicit
		// group agent-use grant. Its system_agent read is confined to the group's
		// own agent by the folded agent gate.
		grant, err := authz.GroupToolGrant("agent.use")
		return err == nil && a.authority.HasGrant(grant) && action == authz.ActionRead && adminScope(scope)
	default:
		return false
	}
}

// actorSkillAllowed is the shared user/delegated-agent rule: list; own
// user/user_agent read/create/write/delete; read shared system/system_agent.
// Admin-managed writes are excluded (only the admin superuser holds those).
func actorSkillAllowed(action authz.Action, scope string, isOwner bool) bool {
	switch action {
	case authz.ActionList:
		return true
	case authz.ActionRead:
		if adminScope(scope) {
			return true
		}
		return userScope(scope) && isOwner
	case authz.ActionCreate, authz.ActionWrite, authz.ActionDelete:
		return userScope(scope) && isOwner
	default:
		return false
	}
}

// authorizeAgent asks the Agent domain for read access, preserving its
// 404-not-found / 403-forbidden visibility split.
func (a *Access) authorizeAgent(ctx context.Context, agentID string) error {
	if agentID == "" {
		return ErrNotFound
	}
	err := a.svc.agents.Authorize(ctx, a.authority, agentID, authz.ActionRead)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, agentaccess.ErrForbidden):
		return ErrForbidden
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}
