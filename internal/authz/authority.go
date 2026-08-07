package authz

import "errors"

// Authority is the immutable, trusted capability every application service
// authorises against. It is a closed value with unexported fields and no
// exported struct literal, so the constructors below are the only way to obtain a
// valid one, and their validation cannot be bypassed. Only trusted entry,
// agent-runtime, and durable-worker adapters may call them (the mint-boundary
// test enforces this); request payloads, paths, model arguments, and channel
// fields cannot.
//
// The zero Authority is invalid and fails closed. Stella is single-tenant, so an
// Authority is pure identity plus two trusted bits — the admin superuser flag and
// one exact dedicated channel binding — never a generic role/grant framework. The
// durable facts each domain's rules need are resolved at decision time, not
// carried here.

// ErrInvalidActor is returned when constructor arguments do not satisfy the
// requested variant (missing required id).
var ErrInvalidActor = errors.New("authz: invalid actor")

// ActorKind is the closed catalog of actor variants an Authority discriminates.
type ActorKind uint8

const (
	// ActorInvalid is the zero value. A zero Authority is invalid and fails
	// closed.
	ActorInvalid ActorKind = iota
	// ActorUser is a human user acting through HTTP or a linked private channel,
	// optionally the admin superuser.
	ActorUser
	// ActorAgent is an agent delegated by a user and confined to one agent,
	// either a live private-session tool call or reconstructed durable work.
	ActorAgent
	// ActorGroupAgent is a group turn executing as one agent inside one group; it
	// carries no user and can never reach user-private capabilities.
	ActorGroupAgent
	// ActorGuest is a durable unlinked channel principal confined to one exact
	// dedicated channel binding.
	ActorGuest
	// ActorSystem is named maintenance / control-plane work. It has no user or
	// admin identity and is never an implicit omnipotent actor.
	ActorSystem
)

var allActorKinds = []ActorKind{ActorUser, ActorAgent, ActorGroupAgent, ActorGuest, ActorSystem}

// AllActorKinds returns the closed actor-kind catalog.
func AllActorKinds() []ActorKind { return append([]ActorKind(nil), allActorKinds...) }

// Valid reports whether the actor kind is a member of the catalog.
func (k ActorKind) Valid() bool { return k >= ActorUser && k <= ActorSystem }

func (k ActorKind) String() string {
	switch k {
	case ActorUser:
		return "user"
	case ActorAgent:
		return "agent"
	case ActorGroupAgent:
		return "group_agent"
	case ActorGuest:
		return "guest"
	case ActorSystem:
		return "system"
	default:
		return "invalid"
	}
}

// UserID, AgentID, GroupID, and Component are distinct string types so a caller
// cannot accidentally pass an agent id where a user id is required. Component
// names the maintenance class of a SystemActor.
type (
	UserID    string
	AgentID   string
	GroupID   string
	GuestID   string
	Component string
)

// Authority is the immutable identity plus trusted attributes, discriminated by
// kind. Owner/executor for a delegated AgentActor are (userID, agentID). A
// GroupAgentActor carries only (groupID, agentID): the triggering group member is
// request/audit attribution resolved by the transport, never part of the
// authority, so it cannot grant that member's private-user capabilities to the
// group turn. admin marks the user superuser; channelBindingID, when set, is the
// one exact dedicated channel binding the Agent PEP may consume.
type Authority struct {
	kind             ActorKind
	userID           UserID
	agentID          AgentID
	groupID          GroupID
	guestID          GuestID
	component        Component
	admin            bool
	channelBindingID string
}

// NewUserAuthority constructs a UserActor authority. Every valid user is an
// ordinary user, optionally the admin superuser. The user id is required.
func NewUserAuthority(user UserID, admin bool) (Authority, error) {
	if user == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorUser, userID: user, admin: admin}, nil
}

// NewChannelAuthority constructs a UserActor authority that additionally holds
// one exact dedicated channel binding. channelID is read from the persisted
// channel configuration by the channel adapter; it is never request-payload
// identity, and it is consumed only by the Agent PEP's dedicated-channel decision.
func NewChannelAuthority(user UserID, admin bool, channelID string) (Authority, error) {
	if user == "" || channelID == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorUser, userID: user, admin: admin, channelBindingID: channelID}, nil
}

// NewAgentAuthority constructs an AgentActor authority delegated by owner and
// confined to executor. Both ids are required; this is also the shape a durable
// worker reconstructs from a persisted owner + executor agent. A delegated agent
// never carries admin or a channel binding.
func NewAgentAuthority(owner UserID, executor AgentID) (Authority, error) {
	if owner == "" || executor == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorAgent, userID: owner, agentID: executor}, nil
}

// NewGroupAgentAuthority constructs a GroupAgentActor authority: one agent
// executing inside one group. It carries no user id and no admin identity, so
// user-private access is structurally impossible.
func NewGroupAgentAuthority(group GroupID, agent AgentID) (Authority, error) {
	if group == "" || agent == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorGroupAgent, groupID: group, agentID: agent}, nil
}

// NewGuestAuthority constructs a durable guest confined to one exact channel
// binding. Both values are required; it carries no user, agent, group, or admin.
func NewGuestAuthority(guest GuestID, channelBindingID string) (Authority, error) {
	if guest == "" || channelBindingID == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorGuest, guestID: guest, channelBindingID: channelBindingID}, nil
}

// NewSystemAuthority constructs a named SystemActor. The component name is
// required. A system actor has no user or admin identity; it is named maintenance
// work, never an implicit omnipotent identity.
func NewSystemAuthority(name Component) (Authority, error) {
	if name == "" {
		return Authority{}, ErrInvalidActor
	}
	return Authority{kind: ActorSystem, component: name}, nil
}

// Valid reports whether the authority is a well-formed member of its variant. It
// mirrors the constructor invariants so a value that somehow bypassed a
// constructor (e.g. a zero value) still fails closed. Only a UserActor may carry
// admin or a channel binding.
func (a Authority) Valid() bool {
	switch a.kind {
	case ActorUser:
		return a.userID != "" && a.agentID == "" && a.groupID == "" && a.guestID == "" && a.component == ""
	case ActorAgent:
		return a.userID != "" && a.agentID != "" && a.groupID == "" && a.guestID == "" && a.component == "" && !a.admin && a.channelBindingID == ""
	case ActorGroupAgent:
		return a.groupID != "" && a.agentID != "" && a.userID == "" && a.guestID == "" && a.component == "" && !a.admin && a.channelBindingID == ""
	case ActorGuest:
		return a.guestID != "" && a.channelBindingID != "" && a.userID == "" && a.agentID == "" && a.groupID == "" && a.component == "" && !a.admin
	case ActorSystem:
		return a.component != "" && a.userID == "" && a.agentID == "" && a.groupID == "" && a.guestID == "" && !a.admin && a.channelBindingID == ""
	default:
		return false
	}
}

// Kind returns the actor variant. A zero Authority returns ActorInvalid.
func (a Authority) Kind() ActorKind { return a.kind }

// UserID returns the owning/acting user for UserActor and AgentActor, empty for
// group/system actors that have no user owner.
func (a Authority) UserID() UserID { return a.userID }

// AgentID returns the confined/executing agent for AgentActor and
// GroupAgentActor, empty otherwise.
func (a Authority) AgentID() AgentID { return a.agentID }

// GroupID returns the group for GroupAgentActor, empty otherwise.
func (a Authority) GroupID() GroupID { return a.groupID }

// GuestID returns the durable guest principal for GuestActor, empty otherwise.
func (a Authority) GuestID() GuestID { return a.guestID }

// Component returns the maintenance class for SystemActor, empty otherwise.
func (a Authority) Component() Component { return a.component }

// IsAdmin reports whether the authority is the admin superuser.
func (a Authority) IsAdmin() bool { return a.admin }

// ChannelBindingID returns the exact dedicated channel binding this authority
// holds, or empty when it holds none. Only a UserActor minted by
// NewChannelAuthority carries one.
func (a Authority) ChannelBindingID() string { return a.channelBindingID }
