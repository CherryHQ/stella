package policy

import (
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
)

// subject is a static rule's actor matcher. Rules are authored in Go, so there
// is no selector parser or runtime validation surface.
type subject struct {
	kinds  []authz.ActorKind
	roles  []authz.Role
	grants []authz.Grant
}

func (s subject) matches(a authz.Authority) bool {
	if len(s.kinds) > 0 && !slices.Contains(s.kinds, a.Kind()) {
		return false
	}
	if len(s.roles) > 0 && !slices.ContainsFunc(s.roles, a.HasRole) {
		return false
	}
	if len(s.grants) > 0 && !slices.ContainsFunc(s.grants, a.HasGrant) {
		return false
	}
	return true
}

// predicate is a fixed comparison in a built-in rule. Missing facts never
// match, preserving fail-closed behavior.
type predicate struct {
	Attr   string
	Op     operator
	Value  string
	Values []string
}

type operator uint8

const (
	opEq operator = iota
	opIn
)

// compiledPolicy is one immutable built-in rule.
type compiledPolicy struct {
	id       string
	subjects subject

	anyResource bool
	resource    authz.ResourceType
	anyAction   bool
	actions     []authz.Action
	predicates  []predicate
}

func (p compiledPolicy) matches(a authz.Authority, req authz.Request) bool {
	if !p.subjects.matches(a) || (!p.anyResource && p.resource != req.Resource().Type()) || (!p.anyAction && !slices.Contains(p.actions, req.Action())) {
		return false
	}
	for _, predicate := range p.predicates {
		value, ok := req.Resource().Attr(predicate.Attr)
		if !ok {
			return false
		}
		switch predicate.Op {
		case opEq:
			if value != predicate.Value {
				return false
			}
		case opIn:
			if !slices.Contains(predicate.Values, value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// builtinPolicies reproduce the current agent decision semantics (auth.seed.go)
// for the shadow scope, plus the admin superuser rule. They are in-code and
// never persisted, and carry explicit subject selectors like any other policy.
// Only the Agent resource is exercised today; other resources are default-deny
// until their owning stack adds built-ins and activates them.
//
// Denial visibility is deliberately the Forbidden default here: per-resource
// Hidden(404)-vs-Forbidden(403) refinement is owned by each domain cutover
// (#709 agent/session, #710 execution, #711 user-capability, #712 platform),
// when typed domain visibility policies land.
func builtinPolicies() []compiledPolicy {
	adminOnly := subject{roles: []authz.Role{authz.RoleAdmin}}
	userOnly := subject{roles: []authz.Role{authz.RoleUser}}
	groupUseGrant, _ := authz.GroupToolGrant("agent.use")
	systemUseGrant, _ := authz.SystemGrant("agent.use")
	agentOnly := subject{kinds: []authz.ActorKind{authz.ActorAgent}}
	groupAgentUse := subject{kinds: []authz.ActorKind{authz.ActorGroupAgent}, grants: []authz.Grant{groupUseGrant}}
	systemUse := subject{kinds: []authz.ActorKind{authz.ActorSystem}, grants: []authz.Grant{systemUseGrant}}
	return []compiledPolicy{
		// Admin holds every action on every resource.
		{
			id:          "builtin:admin-full-access",
			subjects:    adminOnly,
			anyResource: true,
			anyAction:   true,
		},
		// A user may list agents (collection-level parity with legacy agent_list).
		{
			id:       "builtin:user-list-agents",
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionList},
		},
		// A user may read/execute a system-scoped agent.
		{
			id:       "builtin:user-system-agents",
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{
				{Attr: "scope", Op: opEq, Value: "system"},
			},
		},
		// A user may read/execute an agent assigned to them.
		{
			id:       "builtin:user-assigned-agents",
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{
				{Attr: "assigned", Op: opEq, Value: "true"},
			},
		},
		// A durable AgentActor executes only its exact executor agent. Roles do
		// not widen this boundary.
		{
			id:       "builtin:agent-own-executor",
			subjects: agentOnly, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_executor", Op: opEq, Value: "true"}},
		},
		// A group turn has no user role or private grants. It can execute only
		// its exact member agent and only with the explicit group-scoped use grant.
		{
			id:       "builtin:group-agent-use",
			subjects: groupAgentUse, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_executor", Op: opEq, Value: "true"}},
		},
		// Named maintenance is deliberately capability-based, never role-based.
		{
			id:       "builtin:system-agent-use",
			subjects: systemUse, resource: authz.ResourceAgent,
			actions: []authz.Action{authz.ActionRead, authz.ActionExecute},
		},
		// A verified dedicated channel binding may execute only its configured
		// agent. The PEP derives this fact from an exact ChannelBinding grant.
		{
			id:       "builtin:user-dedicated-channel-agent",
			subjects: userOnly, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "dedicated", Op: opEq, Value: "true"}},
		},
		// A user owns their sessions and the workspaces rooted by those sessions.
		// The is_owner bit is derived at the Session/Workspace PEP from immutable
		// authority plus durable conversation facts; routes never supply it.
		{
			id:         "builtin:user-own-sessions",
			subjects:   userOnly,
			resource:   authz.ResourceSession,
			actions:    []authz.Action{authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute, authz.ActionCreate},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		{
			id:       "builtin:user-list-sessions",
			subjects: userOnly,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionList},
		},
		// Durable worker/delegate agents may create/read/execute only sessions
		// whose loaded facts match both their owner and exact executor agent.
		{
			id:       "builtin:agent-own-sessions",
			subjects: agentOnly,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute, authz.ActionCreate, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "is_owner", Op: opEq, Value: "true"},
				{Attr: "is_executor", Op: opEq, Value: "true"},
			},
		},
		// Group turns may create/read/execute only sessions bound to their exact
		// group and member agent, with the explicit group agent-use grant.
		{
			id:       "builtin:group-agent-sessions",
			subjects: groupAgentUse,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute, authz.ActionCreate, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "is_group", Op: opEq, Value: "true"},
				{Attr: "is_same_group", Op: opEq, Value: "true"},
				{Attr: "is_executor", Op: opEq, Value: "true"},
			},
		},
		// Named maintenance/system workers run under an explicit system grant. They
		// are not users and do not inherit admin; the grant is the whole capability.
		{
			id:       "builtin:system-sessions",
			subjects: systemUse,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute, authz.ActionCreate, authz.ActionDelete},
		},
		{
			id:         "builtin:user-own-workspaces",
			subjects:   userOnly,
			resource:   authz.ResourceWorkspace,
			actions:    []authz.Action{authz.ActionRead, authz.ActionList, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		// Any user may create an agent (the transport confines a non-admin to a
		// restricted scope). Collection-level: no per-agent attributes.
		{
			id:       "builtin:user-create-agents",
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionCreate},
		},
		// A user may manage (update/delete) an agent they created. is_creator is
		// resolved at the PEP from the loaded agent's creator and the acting user.
		{
			id:       "builtin:user-manage-own-agents",
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionWrite, authz.ActionDelete, authz.ActionManage},
			predicates: []predicate{
				{Attr: "is_creator", Op: opEq, Value: "true"},
			},
		},
		// A user may list their workflows (collection-level; per-row read filters
		// by is_owner in the same evaluation).
		{
			id:       "builtin:user-list-workflows",
			subjects: userOnly,
			resource: authz.ResourceWorkflow,
			actions:  []authz.Action{authz.ActionList},
		},
		// A user owns the workflows they created (read/create/update/delete/run).
		// is_owner is derived at the PEP from the loaded row and the acting user.
		{
			id:         "builtin:user-own-workflows",
			subjects:   userOnly,
			resource:   authz.ResourceWorkflow,
			actions:    []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		// A delegated agent may list workflows it owns as executor.
		{
			id:       "builtin:agent-list-workflows",
			subjects: agentOnly,
			resource: authz.ResourceWorkflow,
			actions:  []authz.Action{authz.ActionList},
		},
		// A delegated agent may read/save/run only the workflows whose durable
		// facts match both its owner and its exact executor agent.
		{
			id:       "builtin:agent-own-workflows",
			subjects: agentOnly,
			resource: authz.ResourceWorkflow,
			actions:  []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionExecute},
			predicates: []predicate{
				{Attr: "is_owner", Op: opEq, Value: "true"},
				{Attr: "is_executor", Op: opEq, Value: "true"},
			},
		},
		// A user may list their goals (collection-level; per-row read filters by
		// is_owner in the same evaluation).
		{
			id:       "builtin:user-list-goals",
			subjects: userOnly,
			resource: authz.ResourceGoal,
			actions:  []authz.Action{authz.ActionList},
		},
		// A user owns the goals they created (read/create/update/delete/run).
		{
			id:         "builtin:user-own-goals",
			subjects:   userOnly,
			resource:   authz.ResourceGoal,
			actions:    []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		// A delegated agent may list goals it owns as executor.
		{
			id:       "builtin:agent-list-goals",
			subjects: agentOnly,
			resource: authz.ResourceGoal,
			actions:  []authz.Action{authz.ActionList},
		},
		// A delegated agent may act only on goals whose durable facts match both
		// its owner and its exact executor agent.
		{
			id:       "builtin:agent-own-goals",
			subjects: agentOnly,
			resource: authz.ResourceGoal,
			actions:  []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute},
			predicates: []predicate{
				{Attr: "is_owner", Op: opEq, Value: "true"},
				{Attr: "is_executor", Op: opEq, Value: "true"},
			},
		},
		// A user may list their skills (collection-level; per-scope management
		// filters by owner/admin in the same evaluation).
		{
			id:       "builtin:user-list-skills",
			subjects: userOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionList},
		},
		// A user owns the user/user_agent skills they created (read/create/write/
		// delete). is_owner is derived at the PEP from the loaded row (or write
		// target) and the acting user; admin-managed system scopes are excluded.
		{
			id:       "builtin:user-own-skills",
			subjects: userOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}},
				{Attr: "is_owner", Op: opEq, Value: "true"},
			},
		},
		// Any user may read admin-managed system/system_agent skills (they are
		// shared reference procedures); only admins may write them.
		{
			id:       "builtin:user-read-system-skills",
			subjects: userOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionRead},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"system", "system_agent"}},
			},
		},
		// A delegated agent may list skills it owns as the delegating user.
		{
			id:       "builtin:agent-list-skills",
			subjects: agentOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionList},
		},
		// A delegated agent may act only on user/user_agent skills owned by its
		// delegating user (is_owner). It never writes admin-managed system scopes.
		{
			id:       "builtin:agent-own-skills",
			subjects: agentOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}},
				{Attr: "is_owner", Op: opEq, Value: "true"},
			},
		},
		// A delegated agent may read admin-managed system/system_agent skills (they
		// are shared reference procedures loaded during a turn); it never writes
		// them. system_agent rows are additionally confined to the agent by the
		// PEP's folded agent-read gate.
		{
			id:       "builtin:agent-read-system-skills",
			subjects: agentOnly,
			resource: authz.ResourceSkill,
			actions:  []authz.Action{authz.ActionRead},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"system", "system_agent"}},
			},
		},
		// A group turn (GroupAgentActor, no user) may read the shared, non-personal
		// system/system_agent skills it could see before the cutover — those are
		// public reference procedures, not identity-scoped data. It has no user, so
		// user/user_agent skills stay hidden. The group agent-use grant confines a
		// system_agent read to the group's own agent via the folded agent gate.
		{
			id:       "builtin:group-agent-read-system-skills",
			subjects: groupAgentUse, resource: authz.ResourceSkill,
			actions: []authz.Action{authz.ActionRead},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"system", "system_agent"}},
			},
		},

		// ---- #711 Vault ---------------------------------------------------
		// A user may list their vault entries in the user/user_agent scopes; a
		// per-scope read decision filters rows in the same evaluation.
		{
			id:       "builtin:user-list-vault",
			subjects: userOnly, resource: authz.ResourceVault,
			actions:    []authz.Action{authz.ActionList},
			predicates: []predicate{{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}}},
		},
		// A user owns their user/user_agent vault entries. system/system_agent
		// scopes are admin-managed (admin-full-access only); is_owner is derived by
		// the PEP from the entry's owner/agent columns.
		{
			id:       "builtin:user-own-vault",
			subjects: userOnly, resource: authz.ResourceVault,
			actions: []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}},
				{Attr: "is_owner", Op: opEq, Value: "true"},
			},
		},
		// A delegated agent may list its delegating user's user/user_agent vault.
		{
			id:       "builtin:agent-list-vault",
			subjects: agentOnly, resource: authz.ResourceVault,
			actions:    []authz.Action{authz.ActionList},
			predicates: []predicate{{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}}},
		},
		// A delegated agent acts only on its delegating user's user/user_agent vault
		// entries (is_owner). The PEP additionally confines an agent-scoped actor to
		// its own user_agent bucket before setting is_owner.
		{
			id:       "builtin:agent-own-vault",
			subjects: agentOnly, resource: authz.ResourceVault,
			actions: []authz.Action{authz.ActionRead, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{
				{Attr: "scope", Op: opIn, Values: []string{"user", "user_agent"}},
				{Attr: "is_owner", Op: opEq, Value: "true"},
			},
		},
	}
}
