package skills

import "context"

// SkillReadAuthorizer authorizes DB-backed skill reads for one skills-tool
// invocation. It is the narrow, consumer-owned port that lets the tool enforce
// ResourceSkill without importing internal/skillaccess (which imports this
// package); the composition root injects a *skillaccess.Service that satisfies
// it. When no authorizer is injected, DB-backed reads fail closed — see the tool
// read paths, which drop or 404 an unauthorized DB skill rather than serving it.
type SkillReadAuthorizer interface {
	// BeginRead opens exactly one policy evaluation for the read use case from the
	// runtime context identity (the trusted turn/worker identity) and returns a
	// per-row decider. A missing/invalid identity fails closed with an error.
	BeginRead(ctx context.Context) (SkillReadDecision, error)
}

// SkillReadDecision decides individual DB-skill reads within one evaluation.
type SkillReadDecision interface {
	// AllowRead reports whether the actor may read this DB skill row. A policy
	// denial returns (false, nil); an unexpected authorization failure returns a
	// non-nil error.
	AllowRead(ctx context.Context, id, scope, ownerUserID, agentID string) (bool, error)
}

// SkillWriteAuthorizer authorizes DB-backed skill writes (create/patch/deprecate)
// for one skills-tool invocation. Like SkillReadAuthorizer it is consumer-owned
// (no skills→skillaccess cycle) and injected from the composition root; the
// reflect reviewer tool uses it so its prompt-driven create/patch/deprecate are
// each authorized before the store mutation. The agent-facing tool has no write
// actions and so is never given one. When nil, DB writes fail closed.
type SkillWriteAuthorizer interface {
	// BeginWrite opens exactly one policy evaluation for the write use case from
	// the runtime context identity. A missing/invalid identity fails closed.
	BeginWrite(ctx context.Context) (SkillWriteDecision, error)
}

// SkillWriteDecision authorizes individual DB-skill writes within one evaluation.
// A denial or unexpected failure returns a non-nil error (the tool surfaces it as
// a write failure); nil means allowed.
type SkillWriteDecision interface {
	// AllowCreate authorizes minting a new DB skill in scope for agentID.
	AllowCreate(ctx context.Context, scope, agentID string) error
	// AllowWrite authorizes mutating (patch/deprecate) the existing DB skill id;
	// the PEP loads the row and decides against its real durable facts.
	AllowWrite(ctx context.Context, id string) error
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

// isDBSkill reports whether a resolved skill is a DB row (and so ResourceSkill-
// gated). Filesystem project and built-in system skills always carry a Dir from
// the merge/resolve; DB rows never do, so an empty Dir on a DB scope is the
// unambiguous signal.
func isDBSkill(rs ResolvedSkill) bool {
	return rs.Dir == "" && isDBScope(rs.Scope)
}
