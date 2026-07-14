// Package skillaccess owns the direct authorization rules for DB-backed Skill
// resources. Every skill-row read, write, delete, and scope-management decision
// flows through this domain, under a trusted authz.Authority that no
// request/model input can forge:
//   - HTTP transports call the Begin/Authorize* methods with the caller's
//     Authority;
//   - the agent skills tool (reads) and the reflect reviewer tool (reads and its
//     prompt-driven create/patch/deprecate) reach the decisions through the
//     consumer-owned skills.SkillReadAuthorizer / SkillWriteAuthorizer ports
//     (BeginRead / BeginWrite), which reconstruct the turn/worker identity from
//     context — a user or delegated agent, or a confined GroupAgentActor for a
//     group turn;
//   - reflect's staged skill reconciliation and usage curation authorize each
//     write through AuthorizeWorkerWrite under a fresh WorkerAgentAuthority.
//
// Filesystem project/system skills merged read-only by internal/skills are not
// DB rows and are not security-sensitive; they are outside this PEP's scope.
package skillaccess

import (
	"context"
	"errors"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skills"
)

// Errors returned by the Skill access rules. Denials on user-owned scopes are
// opaque (ErrNotFound, 404) so a foreign skill cannot be told from a missing one;
// admin-managed system scopes surface ErrForbidden (403).
var (
	ErrForbidden    = errors.New("skill access forbidden")
	ErrNotFound     = errors.New("skill not found")
	ErrUnavailable  = errors.New("skill authorization unavailable")
	ErrInvalidScope = errors.New("invalid skill scope")
)

// The four durable skill scopes (the DB scope column). system and system_agent
// are admin-managed; user and user_agent are owned by their user.
const (
	ScopeUser        = "user"
	ScopeUserAgent   = "user_agent"
	ScopeSystem      = "system"
	ScopeSystemAgent = "system_agent"
)

// SkillStore is the narrow read port the PEP needs to resolve one skill row by
// id. The skills store has no direct Get-by-id yet, so the PEP scans ListAll —
// the same approach the transports used, correct at current volumes.
type SkillStore interface {
	ListAll(ctx context.Context) ([]skills.Skill, error)
}

// Service is the composition-root-owned Skill authorization domain. It holds the
// narrow skill read port and the Agent-domain gate (to fold an agent-read check
// into a skill decision).
type Service struct {
	store  SkillStore
	agents *agentaccess.Service
}

// NewService constructs the Skill authorization domain. agents is the Agent-domain
// gate; store is the read port used to load a row for a by-id decision.
func NewService(store SkillStore, agents *agentaccess.Service) *Service {
	return &Service{store: store, agents: agents}
}

// The Skill PEP is the read + write authorizer the skills tool consumes through
// its consumer-owned ports (no skills→skillaccess import).
var (
	_ skills.SkillReadAuthorizer  = (*Service)(nil)
	_ skills.SkillWriteAuthorizer = (*Service)(nil)
)

// BeginRead implements skills.SkillReadAuthorizer for the skills tool. It derives
// the trusted turn/worker identity from the runtime context (an agent turn or the
// reflect reviewer's WithUserID+WithAgentID review context, both of which
// ToAuthority resolves to a roleless AgentActor — the same shape as a
// reconstructed WorkerAgentAuthority) and returns a per-row read decider. A
// missing/invalid identity fails closed. Writes never flow through this path.
func (s *Service) BeginRead(ctx context.Context) (skills.SkillReadDecision, error) {
	if s.agents == nil {
		return nil, ErrUnavailable
	}
	authority, err := contextReadAuthority(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return &readDecision{acc: acc}, nil
}

// contextReadAuthority reconstructs the trusted turn/worker authority from the
// runtime context. A user or delegated-agent identity becomes a User/Agent actor
// (its ToAuthority carries the confined executor); a group turn (group id set, no
// user — D9 isolation) becomes a confined GroupAgentActor so its shared
// system/system_agent skill reads stay authorized without ever minting a user
// identity for the group. A context with no user and no group is unauthenticated;
// callers treat that as fail-hidden, not a hard error.
func contextReadAuthority(ctx context.Context) (authz.Authority, error) {
	if authz.UserIDFromContext(ctx) != "" {
		ident, err := authz.FromContext(ctx)
		if err != nil {
			return authz.Authority{}, err
		}
		return ident.ToAuthority()
	}
	if groupID := authz.GroupIDFromContext(ctx); groupID != "" {
		return agentaccess.GroupAgentAuthority(groupID, authz.AgentIDFromContext(ctx))
	}
	return authz.Authority{}, authz.ErrUnauthenticated
}

// BeginWrite implements skills.SkillWriteAuthorizer for the reflect reviewer
// tool. It reconstructs the trusted turn identity from context (the reflect
// review target's WithUserID+WithAgentID → a roleless AgentActor) and authorizes
// each create/patch/deprecate before the store mutation.
func (s *Service) BeginWrite(ctx context.Context) (skills.SkillWriteDecision, error) {
	if s.agents == nil {
		return nil, ErrUnavailable
	}
	authority, err := contextReadAuthority(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return &writeDecision{acc: acc}, nil
}

// writeDecision authorizes individual DB-skill writes within one evaluation.
type writeDecision struct{ acc *Access }

func (d *writeDecision) AllowCreate(ctx context.Context, scope, agentID string) error {
	_, _, err := d.acc.AuthorizeManageScope(ctx, scope, agentID)
	return err
}

func (d *writeDecision) AllowWrite(ctx context.Context, id string) error {
	_, err := d.acc.AuthorizeManageByID(ctx, id, authz.ActionWrite)
	return err
}

// readDecision decides individual DB-skill reads for one turn, mapping a rule
// denial to (false, nil) and only surfacing unexpected failures.
type readDecision struct{ acc *Access }

func (d *readDecision) AllowRead(ctx context.Context, id, scope, ownerUserID, agentID string) (bool, error) {
	err := d.acc.AuthorizeRead(ctx, skills.Skill{ID: id, Scope: scope, UserID: ownerUserID, AgentID: agentID})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden):
		return false, nil
	default:
		return false, err
	}
}

// scopeOwner returns the owner columns a skill row of the given scope carries,
// per the skill table CHECK: user scopes bind the acting user, agent scopes bind
// the agent, system scopes bind neither. ok is false for an unknown scope.
func scopeOwner(scope, actorUserID, agentID string) (uid, aid string, ok bool) {
	switch scope {
	case ScopeUser:
		return actorUserID, "", true
	case ScopeUserAgent:
		return actorUserID, agentID, true
	case ScopeSystem:
		return "", "", true
	case ScopeSystemAgent:
		return "", agentID, true
	default:
		return "", "", false
	}
}

// agentBound reports whether a scope's rows are bound to an agent (and so need
// the folded agent-read gate).
func agentBound(scope string) bool {
	return scope == ScopeUserAgent || scope == ScopeSystemAgent
}

// userScope reports whether a scope is user-owned (user/user_agent). Ownership of
// such rows is required for every action beyond a collection list.
func userScope(scope string) bool {
	return scope == ScopeUser || scope == ScopeUserAgent
}

// adminScope reports whether a scope is admin-managed (system/system_agent). A
// management action on such a scope is decided as ActionManage so only the admin
// superuser policy grants it, and a denial is Forbidden (403) rather than opaque.
func adminScope(scope string) bool {
	return scope == ScopeSystem || scope == ScopeSystemAgent
}
