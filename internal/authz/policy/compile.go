package policy

import (
	"errors"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
)

// ErrInvalidPolicy is returned when a custom-policy row cannot be compiled into
// an evaluable rule (unknown resource/action/effect, or a predicate that fails
// its schema). Such a row can never become active.
var ErrInvalidPolicy = errors.New("authz/policy: invalid policy")

// effect is the allow/deny axis of a policy.
type effect uint8

const (
	effectAllow effect = iota
	effectDeny
)

func parseEffect(s string) (effect, bool) {
	switch s {
	case "allow":
		return effectAllow, true
	case "deny":
		return effectDeny, true
	default:
		return 0, false
	}
}

// The reverse lookups below are built once from the closed catalogs in
// internal/authz, so a persisted resource_type/action/actor-kind/role/grant-kind
// string maps back to its typed catalog member (or fails validation).
var (
	resourceByString  = buildStringTable(authz.AllResourceTypes(), authz.ResourceType.String)
	actionByString    = buildStringTable(authz.AllActions(), authz.Action.String)
	actorKindByString = buildStringTable(authz.AllActorKinds(), authz.ActorKind.String)
	roleByString      = buildStringTable(authz.AllRoles(), authz.Role.String)
	grantKindByString = buildStringTable(authz.AllGrantKinds(), authz.GrantKind.String)
)

// buildStringTable indexes a closed catalog by each member's String() form.
func buildStringTable[T comparable](members []T, str func(T) string) map[string]T {
	m := make(map[string]T, len(members))
	for _, v := range members {
		m[str(v)] = v
	}
	return m
}

func parseResourceType(s string) (authz.ResourceType, bool) {
	rt, ok := resourceByString[s]
	return rt, ok
}

func parseAction(s string) (authz.Action, bool) {
	a, ok := actionByString[s]
	return a, ok
}

func parseActorKind(s string) (authz.ActorKind, bool) {
	k, ok := actorKindByString[s]
	return k, ok
}

func parseRole(s string) (authz.Role, bool) {
	r, ok := roleByString[s]
	return r, ok
}

func parseGrantKind(s string) (authz.GrantKind, bool) {
	k, ok := grantKindByString[s]
	return k, ok
}

// compiledPolicy is an immutable, evaluable rule. Built-in policies and active
// custom rows compile to this shape. A rule matches a request when the actor's
// role gate, the resource type, the action, and every attribute predicate all
// hold.
type compiledPolicy struct {
	id      string
	effect  effect
	allowed authz.Visibility // denial visibility this rule applies (deny rules only)

	// subjects is the typed subject selector: WHICH actors this rule applies to.
	// Every compiled policy (built-in and custom) carries one; there is no
	// implicit "any actor" default.
	subjects Selector

	anyResource bool
	resource    authz.ResourceType

	anyAction bool
	actions   []authz.Action

	predicates []predicate
}

func (p compiledPolicy) matches(a authz.Authority, req authz.Request) bool {
	if !p.subjects.matches(a) {
		return false
	}
	if !p.anyResource && p.resource != req.Resource().Type() {
		return false
	}
	if !p.anyAction && !containsAction(p.actions, req.Action()) {
		return false
	}
	for _, pr := range p.predicates {
		if !evalPredicate(pr, req.Resource()) {
			return false
		}
	}
	return true
}

func containsAction(actions []authz.Action, a authz.Action) bool {
	return slices.Contains(actions, a)
}

// evalPredicate evaluates one predicate against a resource. Every operator
// requires the attribute to be present: an absent attribute never satisfies a
// predicate, so a rule cannot match on a missing fact (fail closed).
func evalPredicate(p predicate, r authz.Resource) bool {
	val, ok := r.Attr(p.Attr)
	if !ok {
		return false
	}
	switch p.Op {
	case opEq:
		return val == p.Value
	case opNeq:
		return val != p.Value
	case opIn:
		return contains(p.Values, val)
	case opNotIn:
		return !contains(p.Values, val)
	default:
		return false
	}
}

func contains(xs []string, v string) bool {
	return slices.Contains(xs, v)
}

// compileCustom turns a validated custom-policy row into a compiledPolicy. It
// re-validates the subject selector, resource, action, effect, and predicates
// against the catalog and schema so a row that somehow reached the active set
// with an unknown value fails closed instead of silently widening access.
func compileCustom(id, resourceType, action, effectStr string, subjects Selector, preds []predicate) (compiledPolicy, error) {
	rt, ok := parseResourceType(resourceType)
	if !ok {
		return compiledPolicy{}, fmt.Errorf("%w: unknown resource_type %q", ErrInvalidPolicy, resourceType)
	}
	act, ok := parseAction(action)
	if !ok {
		return compiledPolicy{}, fmt.Errorf("%w: unknown action %q", ErrInvalidPolicy, action)
	}
	eff, ok := parseEffect(effectStr)
	if !ok {
		return compiledPolicy{}, fmt.Errorf("%w: unknown effect %q", ErrInvalidPolicy, effectStr)
	}
	if err := subjects.validate(); err != nil {
		return compiledPolicy{}, fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
	}
	if err := validatePredicates(rt, preds); err != nil {
		return compiledPolicy{}, fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
	}
	// Agent list/create are collection decisions and have no canonical single
	// agent facts. Reject resource predicates rather than accepting policies
	// whose missing attributes would silently fail open.
	if rt == authz.ResourceAgent && (act == authz.ActionList || act == authz.ActionCreate) && len(preds) != 0 {
		return compiledPolicy{}, fmt.Errorf("%w: agent collection actions cannot use resource predicates", ErrInvalidPolicy)
	}
	return compiledPolicy{
		id:         id,
		effect:     eff,
		allowed:    authz.VisibilityForbidden,
		subjects:   subjects,
		resource:   rt,
		actions:    []authz.Action{act},
		predicates: preds,
	}, nil
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
	adminOnly := NewSubjectBuilder().Roles(authz.RoleAdmin).Build()
	userOnly := NewSubjectBuilder().Roles(authz.RoleUser).Build()
	groupUseGrant, _ := authz.GroupToolGrant("agent.use")
	systemUseGrant, _ := authz.SystemGrant("agent.use")
	agentOnly := NewSubjectBuilder().Kinds(authz.ActorAgent).Build()
	groupAgentUse := NewSubjectBuilder().Kinds(authz.ActorGroupAgent).Grants(groupUseGrant).Build()
	systemUse := NewSubjectBuilder().Kinds(authz.ActorSystem).Grants(systemUseGrant).Build()
	return []compiledPolicy{
		// Admin holds every action on every resource.
		{
			id:          "builtin:admin-full-access",
			effect:      effectAllow,
			subjects:    adminOnly,
			anyResource: true,
			anyAction:   true,
		},
		// A user may list agents (collection-level parity with legacy agent_list).
		{
			id:       "builtin:user-list-agents",
			effect:   effectAllow,
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionList},
		},
		// A user may read/execute a system-scoped agent.
		{
			id:       "builtin:user-system-agents",
			effect:   effectAllow,
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
			effect:   effectAllow,
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
			id: "builtin:agent-own-executor", effect: effectAllow,
			subjects: agentOnly, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_executor", Op: opEq, Value: "true"}},
		},
		// A group turn has no user role or private grants. It can execute only
		// its exact member agent and only with the explicit group-scoped use grant.
		{
			id: "builtin:group-agent-use", effect: effectAllow,
			subjects: groupAgentUse, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "is_executor", Op: opEq, Value: "true"}},
		},
		// Named maintenance is deliberately capability-based, never role-based.
		{
			id: "builtin:system-agent-use", effect: effectAllow,
			subjects: systemUse, resource: authz.ResourceAgent,
			actions: []authz.Action{authz.ActionRead, authz.ActionExecute},
		},
		// A verified dedicated channel binding may execute only its configured
		// agent. The PEP derives this fact from an exact ChannelBinding grant.
		{
			id: "builtin:user-dedicated-channel-agent", effect: effectAllow,
			subjects: userOnly, resource: authz.ResourceAgent,
			actions:    []authz.Action{authz.ActionRead, authz.ActionExecute},
			predicates: []predicate{{Attr: "dedicated", Op: opEq, Value: "true"}},
		},
		// A user owns their sessions and the workspaces rooted by those sessions.
		// The is_owner bit is derived at the Session/Workspace PEP from immutable
		// authority plus durable conversation facts; routes never supply it.
		{
			id:         "builtin:user-own-sessions",
			effect:     effectAllow,
			subjects:   userOnly,
			resource:   authz.ResourceSession,
			actions:    []authz.Action{authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute, authz.ActionCreate},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		{
			id:       "builtin:user-list-sessions",
			effect:   effectAllow,
			subjects: userOnly,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionList},
		},
		// Durable worker/delegate agents may create/read/execute only sessions
		// whose loaded facts match both their owner and exact executor agent.
		{
			id:       "builtin:agent-own-sessions",
			effect:   effectAllow,
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
			effect:   effectAllow,
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
			effect:   effectAllow,
			subjects: systemUse,
			resource: authz.ResourceSession,
			actions:  []authz.Action{authz.ActionRead, authz.ActionExecute, authz.ActionCreate, authz.ActionDelete},
		},
		{
			id:         "builtin:user-own-workspaces",
			effect:     effectAllow,
			subjects:   userOnly,
			resource:   authz.ResourceWorkspace,
			actions:    []authz.Action{authz.ActionRead, authz.ActionList, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete},
			predicates: []predicate{{Attr: "is_owner", Op: opEq, Value: "true"}},
		},
		// Any user may create an agent (the transport confines a non-admin to a
		// restricted scope). Collection-level: no per-agent attributes.
		{
			id:       "builtin:user-create-agents",
			effect:   effectAllow,
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionCreate},
		},
		// A user may manage (update/delete) an agent they created. is_creator is
		// resolved at the PEP from the loaded agent's creator and the acting user.
		{
			id:       "builtin:user-manage-own-agents",
			effect:   effectAllow,
			subjects: userOnly,
			resource: authz.ResourceAgent,
			actions:  []authz.Action{authz.ActionWrite, authz.ActionDelete, authz.ActionManage},
			predicates: []predicate{
				{Attr: "is_creator", Op: opEq, Value: "true"},
			},
		},
	}
}
