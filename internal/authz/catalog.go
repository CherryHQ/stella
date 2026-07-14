package authz

// Closed authorization catalogs.
//
// Every Action, ResourceType, Visibility, ActorKind, and GrantKind is a member
// of a fixed set fixed at compile time. Values outside the set are invalid and
// fail validation — the policy layer built on top of these catalogs is
// default-deny, so an unrecognised catalog value must never silently widen
// access. The zero value of every catalog type is the invalid member, so a
// zero-initialised struct field fails closed rather than aliasing a real
// permission.
//
// This file defines the vocabulary only. The Actor/Authority values that carry
// these catalog members live in actor.go / authority.go / grant.go, and the
// request/decision shapes that consume them live in decision.go.

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

// ResourceType is a closed catalog of the protected domains the Authorizer
// governs. Every protected entry point maps to exactly one of these; a new
// protected domain adds a member here rather than inventing an ad-hoc string.
type ResourceType uint8

const (
	// ResourceInvalid is the zero value and matches no policy.
	ResourceInvalid ResourceType = iota
	ResourceAgent
	ResourceSession
	ResourceWorkspace
	ResourceSkill
	ResourceGoal
	ResourceWorkflow
	ResourceScheduler
	ResourceVault
	ResourceConnection
	ResourceEmail
	ResourceShare
	ResourceRecally
	ResourceUserData
	ResourceProvider
	ResourceSettings
	ResourcePlugin
	ResourceChannel
	// ResourceTool is a pure/public tool capability with no owned resource of
	// its own (the invocation itself is the protected thing).
	ResourceTool
	ResourceUser
	ResourceGroup
	ResourceMembership
	ResourceToken
	ResourceMCP
	ResourceAuth
	ResourceWebhook
	ResourceEmbeddingJob
	// ResourceSystemCatalog is authenticated read-only platform reference data
	// (model list, status, builtin catalog, skill registry). It is owned by the
	// deployment, not a user.
	ResourceSystemCatalog
)

var allResourceTypes = []ResourceType{
	ResourceAgent, ResourceSession, ResourceWorkspace, ResourceSkill,
	ResourceGoal, ResourceWorkflow, ResourceScheduler, ResourceVault,
	ResourceConnection, ResourceEmail, ResourceShare, ResourceRecally,
	ResourceUserData, ResourceProvider, ResourceSettings, ResourcePlugin,
	ResourceChannel, ResourceTool, ResourceUser, ResourceGroup,
	ResourceMembership, ResourceToken, ResourceMCP, ResourceAuth,
	ResourceWebhook, ResourceEmbeddingJob, ResourceSystemCatalog,
}

// AllResourceTypes returns the closed resource catalog.
func AllResourceTypes() []ResourceType {
	return append([]ResourceType(nil), allResourceTypes...)
}

// Valid reports whether the resource type is a member of the catalog.
func (r ResourceType) Valid() bool {
	return r >= ResourceAgent && r <= ResourceSystemCatalog
}

func (r ResourceType) String() string {
	switch r {
	case ResourceAgent:
		return "agent"
	case ResourceSession:
		return "session"
	case ResourceWorkspace:
		return "workspace"
	case ResourceSkill:
		return "skill"
	case ResourceGoal:
		return "goal"
	case ResourceWorkflow:
		return "workflow"
	case ResourceScheduler:
		return "scheduler"
	case ResourceVault:
		return "vault"
	case ResourceConnection:
		return "connection"
	case ResourceEmail:
		return "email"
	case ResourceShare:
		return "share"
	case ResourceRecally:
		return "recally"
	case ResourceUserData:
		return "user_data"
	case ResourceProvider:
		return "provider"
	case ResourceSettings:
		return "settings"
	case ResourcePlugin:
		return "plugin"
	case ResourceChannel:
		return "channel"
	case ResourceTool:
		return "tool"
	case ResourceUser:
		return "user"
	case ResourceGroup:
		return "group"
	case ResourceMembership:
		return "membership"
	case ResourceToken:
		return "token"
	case ResourceMCP:
		return "mcp"
	case ResourceAuth:
		return "auth"
	case ResourceWebhook:
		return "webhook"
	case ResourceEmbeddingJob:
		return "embedding_job"
	case ResourceSystemCatalog:
		return "system_catalog"
	default:
		return "invalid"
	}
}

// Visibility is a decision-visibility catalog: it controls how an authenticated
// denial is surfaced. It is NOT the route-exposure axis (public/private/admin)
// used by transport classification — it is the forbidden-vs-not-found rule the
// enforcement point applies when a decision denies access.
type Visibility uint8

const (
	// VisibilityInvalid is the zero value and never describes a valid decision.
	VisibilityInvalid Visibility = iota
	// VisibilityForbidden reveals that the resource exists; denial maps to 403.
	VisibilityForbidden
	// VisibilityHidden hides the resource's existence; denial maps to 404.
	VisibilityHidden
)

var allVisibilities = []Visibility{VisibilityForbidden, VisibilityHidden}

// AllVisibilities returns the closed visibility catalog.
func AllVisibilities() []Visibility {
	return append([]Visibility(nil), allVisibilities...)
}

// Valid reports whether the visibility is a member of the catalog.
func (v Visibility) Valid() bool {
	return v == VisibilityForbidden || v == VisibilityHidden
}

func (v Visibility) String() string {
	switch v {
	case VisibilityForbidden:
		return "forbidden"
	case VisibilityHidden:
		return "hidden"
	default:
		return "invalid"
	}
}
