package authz

// Migration adapter: legacy runtime authz.Identity -> Authority.
//
// This is one of three single-direction adapters that let the codebase mint an
// Authority from a pre-existing trusted identity during the migration. This one
// lives in authz because authz.Identity is defined here. The other two legacy
// identities are produced by their owning packages, so their adapters live at
// those trusted producer boundaries to avoid pulling heavier packages into this
// pure core and to keep the dependency direction pointing at authz:
//
//   - credential.Principal -> Authority  (internal/credential)
//   - auth.Subject         -> Authority  (internal/auth)
//
// There is deliberately no reverse adapter (Authority -> Identity): Authority is
// the target model, and a reverse path would let new code round-trip back into a
// legacy identity and keep the old decision path alive.

// ToAuthority converts a legacy runtime Identity into an Authority. An
// agent-scoped identity with a bound agent becomes a delegated AgentActor
// (owner = user, executor = agent); a bare user identity becomes a UserActor. A
// group or unauthenticated identity (no user id) has no user-owned Authority and
// returns ErrUnauthenticated — the same fail-closed rule FromContext applies.
//
// The legacy Identity carries no admin flag, so a bare user maps to an ordinary
// (non-admin) UserActor; this adapter only carries identity. Each domain resolves
// the durable facts its rules need at decision time.
func (id Identity) ToAuthority() (Authority, error) {
	if id.UserID == "" {
		return Authority{}, ErrUnauthenticated
	}
	// An agent-scoped identity is confined to a specific agent. If the scope
	// flag is set but the agent id is missing, the confinement is
	// unsatisfiable — fail closed rather than widening to an unconfined
	// UserActor, which would silently escape agent scope.
	if id.AgentScoped {
		if id.AgentID == "" {
			return Authority{}, ErrForbidden
		}
		return NewAgentAuthority(UserID(id.UserID), AgentID(id.AgentID))
	}
	return NewUserAuthority(UserID(id.UserID), false)
}
