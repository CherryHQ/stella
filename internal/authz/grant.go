package authz

import (
	"errors"
	"slices"
)

// Grants are typed, invocation-scoped capabilities attached to an Authority.
// The security-relevant invariant is structural: each GrantKind belongs to a
// privacy class, and the Authority constructors reject grants whose class the
// actor variant may not hold. This makes "a group turn cannot carry a
// user-private capability" a compile-time-shaped, constructor-enforced property
// rather than a runtime convention.

// ErrInvalidGrant is returned when a grant is malformed (unknown kind or empty
// key).
var ErrInvalidGrant = errors.New("authz: invalid grant")

// GrantKind is the closed catalog of grant families.
type GrantKind uint8

const (
	// GrantInvalid is the zero value and authorises nothing.
	GrantInvalid GrantKind = iota
	// GrantPublicTool is a pure/public tool capability holdable by any actor.
	GrantPublicTool
	// GrantGroupTool is an explicit group-scoped invocation capability.
	GrantGroupTool
	// GrantAgentTool is a user-private tool capability, scoped to the holding
	// AgentActor's owner and executor.
	GrantAgentTool
	// GrantEntryScope is an HTTP entry-capability scope (PAT/OAuth
	// resource:action) that gates which capabilities a user credential may
	// reach. It scopes a user's own private capability.
	GrantEntryScope
	// GrantChannelBinding is an invocation-scoped grant to execute a user's
	// dedicated channel's configured agent. It is not a persistent assignment
	// and is user-private.
	GrantChannelBinding
	// GrantSystemMaintenance is a named minimum grant for one SystemActor
	// maintenance class.
	GrantSystemMaintenance
)

var allGrantKinds = []GrantKind{
	GrantPublicTool, GrantGroupTool, GrantAgentTool,
	GrantEntryScope, GrantChannelBinding, GrantSystemMaintenance,
}

// AllGrantKinds returns the closed grant-kind catalog.
func AllGrantKinds() []GrantKind {
	return append([]GrantKind(nil), allGrantKinds...)
}

// Valid reports whether the grant kind is a member of the catalog.
func (k GrantKind) Valid() bool {
	return k >= GrantPublicTool && k <= GrantSystemMaintenance
}

func (k GrantKind) String() string {
	switch k {
	case GrantPublicTool:
		return "public_tool"
	case GrantGroupTool:
		return "group_tool"
	case GrantAgentTool:
		return "agent_tool"
	case GrantEntryScope:
		return "entry_scope"
	case GrantChannelBinding:
		return "channel_binding"
	case GrantSystemMaintenance:
		return "system_maintenance"
	default:
		return "invalid"
	}
}

// grantClass is the privacy class that decides which actor variants may hold a
// grant kind. It is not exported: callers reason in GrantKinds, and the class
// mapping is the internal enforcement table.
type grantClass uint8

const (
	classPublic      grantClass = iota // any actor
	classGroup                         // group / group-agent actors
	classUserPrivate                   // user / agent actors
	classSystem                        // system actors only
	classNone                          // sentinel: held by no actor variant
)

func (k GrantKind) class() grantClass {
	switch k {
	case GrantPublicTool:
		return classPublic
	case GrantGroupTool:
		return classGroup
	case GrantAgentTool, GrantEntryScope, GrantChannelBinding:
		return classUserPrivate
	case GrantSystemMaintenance:
		return classSystem
	default:
		// An unknown/unclassified kind maps to a sentinel class that no actor's
		// allowedClasses contains, so every actor check rejects it. classSystem
		// would be wrong here — it would let a SystemActor hold an unknown grant.
		return classNone
	}
}

// Grant is an immutable typed capability. Key identifies the specific capability
// within the kind (a tool name, a resource:action scope, a channel id, or a
// maintenance-class name); it is required for every kind.
type Grant struct {
	kind GrantKind
	key  string
}

// NewGrant constructs a validated grant. It rejects an unknown kind or empty
// key. The specific NewXGrant helpers below are the ergonomic entry points.
func NewGrant(kind GrantKind, key string) (Grant, error) {
	if !kind.Valid() || key == "" {
		return Grant{}, ErrInvalidGrant
	}
	return Grant{kind: kind, key: key}, nil
}

// PublicToolGrant, GroupToolGrant, AgentToolGrant, EntryScopeGrant,
// ChannelBindingGrant, and SystemGrant are typed constructors that make the
// grant kind explicit at the call site.
func PublicToolGrant(tool string) (Grant, error) { return NewGrant(GrantPublicTool, tool) }
func GroupToolGrant(tool string) (Grant, error)  { return NewGrant(GrantGroupTool, tool) }
func AgentToolGrant(tool string) (Grant, error)  { return NewGrant(GrantAgentTool, tool) }
func EntryScopeGrant(scope string) (Grant, error) {
	return NewGrant(GrantEntryScope, scope)
}

func ChannelBindingGrant(channelID string) (Grant, error) {
	return NewGrant(GrantChannelBinding, channelID)
}

func SystemGrant(maintenanceClass string) (Grant, error) {
	return NewGrant(GrantSystemMaintenance, maintenanceClass)
}

// Kind returns the grant family.
func (g Grant) Kind() GrantKind { return g.kind }

// Key returns the capability key within the kind.
func (g Grant) Key() string { return g.key }

// Valid reports whether the grant is well-formed.
func (g Grant) Valid() bool { return g.kind.Valid() && g.key != "" }

// GrantSet is an immutable, deduplicated set of grants. Its backing slice is
// never shared with callers: NewGrantSet copies its input and Grants returns a
// copy.
type GrantSet struct {
	grants []Grant
}

// NewGrantSet validates and copies grants into an immutable set, dropping exact
// duplicates. It returns ErrInvalidGrant on the first malformed grant.
func NewGrantSet(grants ...Grant) (GrantSet, error) {
	if len(grants) == 0 {
		return GrantSet{}, nil
	}
	out := make([]Grant, 0, len(grants))
	seen := make(map[Grant]struct{}, len(grants))
	for _, g := range grants {
		if !g.Valid() {
			return GrantSet{}, ErrInvalidGrant
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return GrantSet{grants: out}, nil
}

// Grants returns a defensive copy of the grant set.
func (s GrantSet) Grants() []Grant {
	return append([]Grant(nil), s.grants...)
}

// Len returns the number of grants.
func (s GrantSet) Len() int { return len(s.grants) }

// Has reports whether an exact grant is present.
func (s GrantSet) Has(g Grant) bool {
	return slices.Contains(s.grants, g)
}

// allowedClasses returns the grant classes an actor kind may hold.
func allowedClasses(kind ActorKind) map[grantClass]bool {
	switch kind {
	case ActorUser, ActorAgent:
		return map[grantClass]bool{classPublic: true, classUserPrivate: true}
	case ActorGroup, ActorGroupAgent:
		return map[grantClass]bool{classPublic: true, classGroup: true}
	case ActorSystem:
		return map[grantClass]bool{classSystem: true}
	default:
		return nil
	}
}

// checkGrantsForActor rejects a grant set that carries a class the actor kind
// may not hold. This is where "a group turn cannot hold a user-private grant"
// and "a system actor holds only named system grants" are enforced.
func checkGrantsForActor(kind ActorKind, s GrantSet) error {
	allowed := allowedClasses(kind)
	if allowed == nil {
		return ErrInvalidActor
	}
	for _, g := range s.grants {
		if !allowed[g.kind.class()] {
			return ErrGrantNotAllowed
		}
	}
	return nil
}
