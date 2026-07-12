// Package skillaccess is the sole policy-enforcement point for DB-backed Skill
// resources. The HTTP transports and the agent skills tool pass a trusted
// authz.Authority and never a scoped query; every skill-row read, write, delete,
// and scope-management decision flows through one Authorizer evaluation here.
//
// Filesystem project/system skills merged read-only by internal/skills are not
// DB rows and are not security-sensitive; they are outside this PEP's scope.
package skillaccess

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skills"
)

// Errors returned by the Skill policy-enforcement point. Denials on user-owned
// scopes are opaque (ErrNotFound, 404) so a foreign skill cannot be told from a
// missing one; admin-managed system scopes surface ErrForbidden (403), matching
// the pre-cutover contract.
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

// Service is the composition-root-owned Skill PEP. It holds the narrow skill
// read port, the agent PEP (to fold an agent-read gate into this evaluation),
// and the unified Authorizer.
type Service struct {
	store  SkillStore
	agents *agentaccess.Service
	authz  authz.Authorizer
}

// NewService constructs the Skill PEP. agents + authz are the policy-enforcement
// dependencies; store is the read port used to load a row for a by-id decision.
func NewService(store SkillStore, agents *agentaccess.Service, az authz.Authorizer) *Service {
	return &Service{store: store, agents: agents, authz: az}
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

// adminScope reports whether a scope is admin-managed (system/system_agent). A
// management action on such a scope is decided as ActionManage so only the admin
// superuser policy grants it, and a denial is Forbidden (403) rather than opaque.
func adminScope(scope string) bool {
	return scope == ScopeSystem || scope == ScopeSystemAgent
}

// skillSource extracts the install source recorded in a skill's metadata, if any,
// so a custom policy may match on it. It is informational; no built-in uses it.
func skillSource(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var m struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(metadata, &m)
	return m.Source
}
