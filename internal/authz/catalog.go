package authz

// Closed authorization catalogs.
//
// Action and ActorKind are each a member of a fixed set fixed at compile time.
// Values outside the set are invalid and fail validation — every domain's rules
// are default-deny, so an unrecognised catalog value must never silently widen
// access. The zero value of every catalog type is the invalid member, so a
// zero-initialised struct field fails closed rather than aliasing a real
// permission.
//
// This file defines the shared Action vocabulary. Each domain owns its own static
// rules (agent/session/skill/vault/scheduler/…); the Action verbs are the common
// cross-domain language those rules speak. The ActorKind/Authority values that
// carry these catalog members live in authority.go.

// Action is a closed catalog of the verbs an Authority can be authorised to
// perform on a resource.
type Action uint8

const (
	// ActionInvalid is the zero value and never authorises anything.
	ActionInvalid Action = iota
	// ActionRead reads a single resource's content or configuration.
	ActionRead
	// ActionList enumerates resources of a type within the caller's scope.
	ActionList
	// ActionCreate creates a new resource.
	ActionCreate
	// ActionWrite mutates an existing resource (update/replace).
	ActionWrite
	// ActionDelete removes a resource.
	ActionDelete
	// ActionExecute runs a command/turn/job against a resource (run, cancel,
	// send, archive, and other state-transitioning commands collapse to this
	// verb at the catalog level; the specific command is a request fact).
	ActionExecute
	// ActionManage is a control-plane administrative action over a resource
	// class (settings, membership, provider registration).
	ActionManage
	// ActionUse invokes a resource as a capability without reading or mutating
	// it — using an agent to run a turn, using a connection to call an API.
	ActionUse
)

var allActions = []Action{
	ActionRead, ActionList, ActionCreate, ActionWrite,
	ActionDelete, ActionExecute, ActionManage, ActionUse,
}

// AllActions returns the closed action catalog.
func AllActions() []Action { return append([]Action(nil), allActions...) }

// Valid reports whether the action is a member of the catalog.
func (a Action) Valid() bool { return a >= ActionRead && a <= ActionUse }

func (a Action) String() string {
	switch a {
	case ActionRead:
		return "read"
	case ActionList:
		return "list"
	case ActionCreate:
		return "create"
	case ActionWrite:
		return "write"
	case ActionDelete:
		return "delete"
	case ActionExecute:
		return "execute"
	case ActionManage:
		return "manage"
	case ActionUse:
		return "use"
	default:
		return "invalid"
	}
}
