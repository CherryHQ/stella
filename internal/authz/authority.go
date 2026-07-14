package authz

import (
	"errors"
	"slices"
)

// Authority is the immutable capability an application service authorises
// against. It binds a closed Actor identity to an immutable RoleSet and
// GrantSet. Only trusted entry, agent-runtime, and durable-worker adapters may
// construct one (dependency rules enforce this); request payloads, paths, model
// arguments, and channel fields cannot.
//
// The zero Authority is invalid and fails closed. The type has unexported
// fields and no exported struct literal, so the constructors below are the only
// way to obtain a valid value, and their validation cannot be bypassed.

var (
	// ErrInvalidActor is returned when actor fields do not satisfy the
	// requested variant (missing required id, or a foreign id set).
	ErrInvalidActor = errors.New("authz: invalid actor")
	// ErrGrantNotAllowed is returned when a grant's privacy class is not
	// holdable by the actor variant (e.g. a user-private grant on a group turn).
	ErrGrantNotAllowed = errors.New("authz: grant not allowed for actor")
	// ErrInvalidRole is returned when a role is not a member of the role
	// catalog.
	ErrInvalidRole = errors.New("authz: invalid role")
	// ErrSystemActorNeedsGrant is returned when a SystemActor is constructed
	// without at least one named grant. There is no omnipotent implicit system
	// actor.
	ErrSystemActorNeedsGrant = errors.New("authz: system actor requires a named grant")
	// ErrRolesNotAllowed is returned when roles are attached to an actor variant
	// that has no user/admin role — group ingress and group turns carry no role.
	ErrRolesNotAllowed = errors.New("authz: roles not allowed for actor")
)

// Role is the closed catalog of authorization roles. Stella is single-tenant,
// so the role set is small and fixed; an unknown role fails validation.
type Role uint8

const (
	// RoleInvalid is the zero value and grants nothing.
	RoleInvalid Role = iota
	// RoleUser is an ordinary user.
	RoleUser
	// RoleAdmin is a deployment administrator.
	RoleAdmin
)

var allRoles = []Role{RoleUser, RoleAdmin}

// AllRoles returns the closed role catalog.
func AllRoles() []Role { return append([]Role(nil), allRoles...) }

// Valid reports whether the role is a member of the catalog.
func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

func (r Role) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleAdmin:
		return "admin"
	default:
		return "invalid"
	}
}

// RoleSet is an immutable, deduplicated set of roles. Like GrantSet it never
// shares its backing slice with callers.
type RoleSet struct {
	roles []Role
}

// NewRoleSet validates and copies roles into an immutable set, dropping
// duplicates. It returns ErrInvalidRole on the first unknown role.
func NewRoleSet(roles ...Role) (RoleSet, error) {
	if len(roles) == 0 {
		return RoleSet{}, nil
	}
	out := make([]Role, 0, len(roles))
	seen := make(map[Role]struct{}, len(roles))
	for _, r := range roles {
		if !r.Valid() {
			return RoleSet{}, ErrInvalidRole
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return RoleSet{roles: out}, nil
}

// Roles returns a defensive copy of the role set.
func (s RoleSet) Roles() []Role { return append([]Role(nil), s.roles...) }

// Has reports whether a role is present.
func (s RoleSet) Has(r Role) bool {
	return slices.Contains(s.roles, r)
}

// Len returns the number of roles.
func (s RoleSet) Len() int { return len(s.roles) }

// Authority binds an actor to its roles and grants. All fields are unexported
// and immutable after construction.
type Authority struct {
	actor  Actor
	roles  RoleSet
	grants GrantSet
}

// newAuthority is the shared constructor tail: it validates the assembled actor
// and checks the grant classes against the actor variant. RoleSet/GrantSet are
// already immutable copies by the time they reach here.
func newAuthority(actor Actor, roles RoleSet, grants GrantSet) (Authority, error) {
	if !actor.Valid() {
		return Authority{}, ErrInvalidActor
	}
	if err := checkGrantsForActor(actor.kind, grants); err != nil {
		return Authority{}, err
	}
	return Authority{actor: actor, roles: roles, grants: grants}, nil
}

// NewUserAuthority constructs a UserActor authority. The user id is required.
func NewUserAuthority(user UserID, roles RoleSet, grants GrantSet) (Authority, error) {
	if user == "" {
		return Authority{}, ErrInvalidActor
	}
	return newAuthority(Actor{kind: ActorUser, userID: user}, roles, grants)
}

// NewAgentAuthority constructs an AgentActor authority delegated by owner and
// confined to executor. Both ids are required; this is also the shape a durable
// worker reconstructs from a persisted owner + executor agent.
func NewAgentAuthority(owner UserID, executor AgentID, roles RoleSet, grants GrantSet) (Authority, error) {
	if owner == "" || executor == "" {
		return Authority{}, ErrInvalidActor
	}
	return newAuthority(Actor{kind: ActorAgent, userID: owner, agentID: executor}, roles, grants)
}

// NewGroupAuthority constructs a GroupActor authority for group ingress. The
// group id is required; there is no user owner and no user/admin role, so a
// non-empty RoleSet is rejected.
func NewGroupAuthority(group GroupID, roles RoleSet, grants GrantSet) (Authority, error) {
	if group == "" {
		return Authority{}, ErrInvalidActor
	}
	if roles.Len() != 0 {
		return Authority{}, ErrRolesNotAllowed
	}
	return newAuthority(Actor{kind: ActorGroup, groupID: group}, roles, grants)
}

// NewGroupAgentAuthority constructs a GroupAgentActor authority: one agent
// executing inside one group. It carries no user id and no user/admin role (a
// non-empty RoleSet is rejected), and grant-class checking rejects any
// user-private grant. The triggering group member is not an argument here — it
// is audit attribution resolved by the transport, not part of the authority.
func NewGroupAgentAuthority(group GroupID, agent AgentID, roles RoleSet, grants GrantSet) (Authority, error) {
	if group == "" || agent == "" {
		return Authority{}, ErrInvalidActor
	}
	if roles.Len() != 0 {
		return Authority{}, ErrRolesNotAllowed
	}
	return newAuthority(Actor{kind: ActorGroupAgent, groupID: group, agentID: agent}, roles, grants)
}

// NewSystemAuthority constructs a named SystemActor. The component name and at
// least one grant are required, and every grant must be a system grant. There
// is no implicit omnipotent system actor.
func NewSystemAuthority(name Component, grants GrantSet) (Authority, error) {
	if name == "" {
		return Authority{}, ErrInvalidActor
	}
	if grants.Len() == 0 {
		return Authority{}, ErrSystemActorNeedsGrant
	}
	return newAuthority(Actor{kind: ActorSystem, component: name}, RoleSet{}, grants)
}

// Valid reports whether the authority was produced by a constructor and remains
// well-formed. A zero Authority is invalid.
func (a Authority) Valid() bool {
	if !a.actor.Valid() {
		return false
	}
	return checkGrantsForActor(a.actor.kind, a.grants) == nil
}

// Actor returns the immutable actor identity.
func (a Authority) Actor() Actor { return a.actor }

// Kind is a shortcut for a.Actor().Kind().
func (a Authority) Kind() ActorKind { return a.actor.kind }

// Roles returns a defensive copy of the authority's roles.
func (a Authority) Roles() []Role { return a.roles.Roles() }

// HasRole reports whether the authority holds a role.
func (a Authority) HasRole(r Role) bool { return a.roles.Has(r) }

// IsAdmin reports whether the authority holds the admin role.
func (a Authority) IsAdmin() bool { return a.roles.Has(RoleAdmin) }

// Grants returns a defensive copy of the authority's grants.
func (a Authority) Grants() []Grant { return a.grants.Grants() }

// HasGrant reports whether the authority holds an exact grant.
func (a Authority) HasGrant(g Grant) bool { return a.grants.Has(g) }
