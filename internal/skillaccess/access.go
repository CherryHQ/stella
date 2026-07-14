package skillaccess

import (
	"context"
	"errors"
	"fmt"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/skills"
)

// Access is one Skill use case bound to exactly one Authorizer evaluation. Do
// not retain it across use cases: a fresh Begin re-reads the policy revision so a
// since-revoked grant stops the next decision.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	// scopeAgentID is the executor confinement: empty for a plain user actor, the
	// bound agent for a delegated AgentActor (which may only touch its own).
	scopeAgentID string
}

// Begin opens exactly one evaluation for one Skill use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s.authz == nil || s.agents == nil {
		return nil, fmt.Errorf("%w: authorizer not configured", ErrUnavailable)
	}
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(actor.UserID()), scopeAgentID: agentID}, nil
}

// AuthorizeManageScope authorizes managing a whole scope bucket — create,
// install, upload, or the scope-keyed management list — and returns the owner
// columns a row of that scope carries. user/user_agent require the acting user to
// be the owner (always true here); system/system_agent require the admin
// superuser. When agentID is non-empty the caller's read access to that agent is
// folded into this same evaluation (the former requireAgentAccess gate).
//
// The management-list reuses this create-authority decision deliberately: the
// pre-cutover resolveSkillManageScope was the single gate for both listing and
// creating a scope, so a custom deny that hides a scope also hides its list.
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
	facts := policy.SkillFacts{Scope: scope, Owner: uid, Agent: aid, IsOwner: uid != "" && uid == a.userID}
	req, err := policy.SkillRequest(authz.ActionCreate, ownerKey(uid), uid, facts)
	if err != nil {
		return "", "", ErrForbidden
	}
	if err := a.decide(scope, req); err != nil {
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
	facts := policy.SkillFacts{
		Scope:   sk.Scope,
		Owner:   sk.UserID,
		Agent:   sk.AgentID,
		IsOwner: a.userID != "" && a.userID == sk.UserID,
	}
	req, err := policy.SkillRequest(decided, sk.ID, sk.UserID, facts)
	if err != nil {
		return ErrForbidden
	}
	if err := a.decide(sk.Scope, req); err != nil {
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
// (builtin user-read-system-skills / agent-read-system-skills) while a custom
// deny or a since-revoked agent grant still hides the row. For agent-bound scopes
// the caller's read access to the skill's agent is folded into this evaluation.
// A user-scope denial is opaque (ErrNotFound); a system-scope denial is
// ErrForbidden — the caller decides whether to skip (list) or 404 (single load).
func (a *Access) AuthorizeRead(ctx context.Context, sk skills.Skill) error {
	// No a.userID != "" guard here: Begin already validated the authority, and a
	// GroupAgentActor (a group turn) legitimately has no user. Its read visibility
	// is decided by policy (system/system_agent only) plus the folded agent gate.
	// A delegated agent never reads another agent's bound skills.
	if a.scopeAgentID != "" && sk.AgentID != "" && a.scopeAgentID != sk.AgentID {
		return ErrNotFound
	}
	facts := policy.SkillFacts{
		Scope:   sk.Scope,
		Owner:   sk.UserID,
		Agent:   sk.AgentID,
		IsOwner: a.userID != "" && a.userID == sk.UserID,
	}
	req, err := policy.SkillRequest(authz.ActionRead, sk.ID, sk.UserID, facts)
	if err != nil {
		return ErrForbidden
	}
	if err := a.decide(sk.Scope, req); err != nil {
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
// agentID) — one evaluation per operation. create=true authorizes minting a new
// row (scope create); otherwise it authorizes writing the existing skillID. It is
// the single PEP for reflect's staged skill reconciliation and usage curation, so
// a since-revoked agent grant or a custom deny stops the write.
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
	req, err := policy.SkillListRequest()
	if err != nil {
		return ErrForbidden
	}
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		return ErrForbidden
	}
	return nil
}

// AuthorizeAgent folds the route agent's read gate into this evaluation. The
// agent-scoped skill endpoints reach a skill through /api/agents/{id}/skills, so
// the path agent {id} is gated under the same revision as the skill decision, for
// every scope — including user and system DB skills whose own row is not
// agent-bound. This replaces the preliminary requireAgentAccess split evaluation.
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

// decide runs one resource decision and maps a denial to the scope's visibility:
// admin-managed scopes are Forbidden (403), user-owned scopes are opaque (404).
func (a *Access) decide(scope string, req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		if adminScope(scope) {
			return ErrForbidden
		}
		return ErrNotFound
	}
	return nil
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

// ownerKey is the resource id for a scope-level create decision: the owning user
// for user scopes, a stable placeholder otherwise. Predicates match on the durable
// facts (scope/is_owner), not the id.
func ownerKey(uid string) string {
	if uid != "" {
		return uid
	}
	return "system"
}
