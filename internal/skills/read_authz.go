package skills

import "context"

// SkillReadAuthorizer authorizes DB-backed skill reads for one skills-tool
// invocation. It is the narrow, consumer-owned port that lets the tool use the
// Skill domain rules without importing internal/skillaccess (which imports this
// package); the composition root injects a *skillaccess.Service that satisfies
// it. When no authorizer is injected, DB-backed reads fail closed — see the tool
// read paths, which drop or 404 an unauthorized DB skill rather than serving it.
type SkillReadAuthorizer interface {
	// BeginRead binds the trusted turn/worker identity to a per-row decider. A
	// missing or invalid identity fails closed with an error.
	BeginRead(ctx context.Context) (SkillReadDecision, error)
}

// SkillReadDecision decides individual DB-skill reads within one invocation.
type SkillReadDecision interface {
	// AllowRead reports whether the actor may read this DB skill row. A denial
	// returns (false, nil); an unexpected authorization failure returns a
	// non-nil error.
	AllowRead(ctx context.Context, id, scope, ownerUserID, agentID string) (bool, error)
}

// isDBScope reports whether a scope names one of the four DB-backed skill scopes
// (as opposed to a filesystem project/built-in skill).
func isDBScope(scope string) bool {
	switch scope {
	case "user", "user_agent", "system", "system_agent":
		return true
	}
	return false
}

// isDBSkill reports whether a resolved skill is a mutable store identity governed
// by Skill domain access rather than an immutable project or builtin snapshot.
func isDBSkill(rs ResolvedSkill) bool {
	return !rs.IsImmutable() && isDBScope(rs.Scope)
}
