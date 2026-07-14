package authz

// Actor is the closed, immutable identity half of an Authority. It is a value
// with unexported fields: the only way to obtain a populated one is through the
// Authority constructors in authority.go, which validate the variant. There is
// deliberately no Actor interface and no exported struct literal, so no caller
// can fabricate a typed-nil actor or a mixed identity by filling optional IDs.
//
// The logical variants are UserActor, AgentActor, GroupActor, GroupAgentActor,
// and SystemActor, discriminated by kind. Each variant populates only the
// fields its kind defines; the constructors reject any other combination.

// ActorKind is the closed catalog of actor variants.
type ActorKind uint8

const (
	// ActorInvalid is the zero value. A zero Actor (and therefore a zero
	// Authority) is invalid and fails closed.
	ActorInvalid ActorKind = iota
	// ActorUser is a human user acting through HTTP or a linked private
	// channel.
	ActorUser
	// ActorAgent is an agent delegated by a user and confined to one agent,
	// either a live private-session tool call or reconstructed durable work.
	ActorAgent
	// ActorGroup is group ingress: a group context with no single user owner.
	ActorGroup
	// ActorGroupAgent is a group turn executing as one agent inside one group;
	// it can never reach user-private capabilities.
	ActorGroupAgent
	// ActorSystem is named maintenance / control-plane work with explicit
	// grants. It is never an implicit omnipotent identity.
	ActorSystem
)

var allActorKinds = []ActorKind{
	ActorUser, ActorAgent, ActorGroup, ActorGroupAgent, ActorSystem,
}

// AllActorKinds returns the closed actor-kind catalog.
func AllActorKinds() []ActorKind {
	return append([]ActorKind(nil), allActorKinds...)
}

// Valid reports whether the actor kind is a member of the catalog.
func (k ActorKind) Valid() bool { return k >= ActorUser && k <= ActorSystem }

func (k ActorKind) String() string {
	switch k {
	case ActorUser:
		return "user"
	case ActorAgent:
		return "agent"
	case ActorGroup:
		return "group"
	case ActorGroupAgent:
		return "group_agent"
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
	Component string
)

// Actor is the immutable identity discriminated by Kind. Owner/executor for a
// durable AgentActor are (userID, agentID). A GroupAgentActor carries only
// (groupID, agentID): the triggering group member is request/audit attribution
// resolved by the transport, never part of the actor, so it cannot grant that
// member's private-user capabilities to the group turn.
type Actor struct {
	kind      ActorKind
	userID    UserID
	agentID   AgentID
	groupID   GroupID
	component Component
}

// Kind returns the actor variant. A zero Actor returns ActorInvalid.
func (a Actor) Kind() ActorKind { return a.kind }

// UserID returns the owning/acting user for UserActor and AgentActor, and empty
// for group/system actors that have no user owner.
func (a Actor) UserID() UserID { return a.userID }

// AgentID returns the confined/executing agent for AgentActor and
// GroupAgentActor, and empty otherwise.
func (a Actor) AgentID() AgentID { return a.agentID }

// GroupID returns the group for GroupActor and GroupAgentActor, empty otherwise.
func (a Actor) GroupID() GroupID { return a.groupID }

// Component returns the maintenance class for SystemActor, empty otherwise.
func (a Actor) Component() Component { return a.component }

// Valid reports whether the actor is a well-formed member of its variant. It
// mirrors the constructor invariants so a value that somehow bypassed a
// constructor (e.g. a zero value) still fails closed.
func (a Actor) Valid() bool {
	switch a.kind {
	case ActorUser:
		return a.userID != "" && a.agentID == "" && a.groupID == "" && a.component == ""
	case ActorAgent:
		return a.userID != "" && a.agentID != "" && a.groupID == "" && a.component == ""
	case ActorGroup:
		return a.groupID != "" && a.userID == "" && a.agentID == "" && a.component == ""
	case ActorGroupAgent:
		return a.groupID != "" && a.agentID != "" && a.userID == "" && a.component == ""
	case ActorSystem:
		return a.component != "" && a.userID == "" && a.agentID == "" && a.groupID == ""
	default:
		return false
	}
}
