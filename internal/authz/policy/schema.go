package policy

import "github.com/CherryHQ/stella/internal/authz"

// The fact types below are assembled only by domain PEPs from immutable
// Authority and loaded durable state. They are intentionally private to this
// static evaluator's known rules; transport input never supplies them.

type AgentFacts struct {
	Scope      string
	Assigned   bool
	Creator    string
	IsCreator  bool
	IsExecutor bool
	Dedicated  bool
	Status     string
}

func AgentResource(id, ownerID string, facts AgentFacts) (authz.Resource, error) {
	return resource(authz.ResourceAgent, id, ownerID, map[string]string{
		"scope":       facts.Scope,
		"assigned":    boolString(facts.Assigned),
		"creator":     facts.Creator,
		"is_creator":  boolString(facts.IsCreator),
		"is_executor": boolString(facts.IsExecutor),
		"dedicated":   boolString(facts.Dedicated),
		"status":      facts.Status,
	})
}

type SessionFacts struct {
	Owner       string
	Agent       string
	Kind        string
	State       string
	IsOwner     bool
	IsExecutor  bool
	IsGroup     bool
	IsSameGroup bool
}

func SessionResource(id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return sessionResource(authz.ResourceSession, id, ownerID, facts)
}

func WorkspaceResource(id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return sessionResource(authz.ResourceWorkspace, id, ownerID, facts)
}

func sessionResource(typ authz.ResourceType, id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return resource(typ, id, ownerID, map[string]string{
		"owner":         facts.Owner,
		"agent":         facts.Agent,
		"kind":          facts.Kind,
		"state":         facts.State,
		"is_owner":      boolString(facts.IsOwner),
		"is_executor":   boolString(facts.IsExecutor),
		"is_group":      boolString(facts.IsGroup),
		"is_same_group": boolString(facts.IsSameGroup),
	})
}

type GoalFacts struct {
	Owner      string
	Agent      string
	State      string
	IsOwner    bool
	IsExecutor bool
}

func GoalResource(id, ownerID string, facts GoalFacts) (authz.Resource, error) {
	return ownerExecutorResource(authz.ResourceGoal, id, ownerID, facts.Owner, facts.Agent, facts.State, facts.IsOwner, facts.IsExecutor)
}

type WorkflowFacts struct {
	Owner      string
	Agent      string
	State      string
	IsOwner    bool
	IsExecutor bool
}

func WorkflowResource(id, ownerID string, facts WorkflowFacts) (authz.Resource, error) {
	return ownerExecutorResource(authz.ResourceWorkflow, id, ownerID, facts.Owner, facts.Agent, facts.State, facts.IsOwner, facts.IsExecutor)
}

type SchedulerFacts struct {
	Owner      string
	Agent      string
	Kind       string
	State      string
	IsOwner    bool
	IsExecutor bool
}

func SchedulerResource(id, ownerID string, facts SchedulerFacts) (authz.Resource, error) {
	return resource(authz.ResourceScheduler, id, ownerID, map[string]string{
		"owner":       facts.Owner,
		"agent":       facts.Agent,
		"kind":        facts.Kind,
		"state":       facts.State,
		"is_owner":    boolString(facts.IsOwner),
		"is_executor": boolString(facts.IsExecutor),
	})
}

type SkillFacts struct {
	Scope   string
	Owner   string
	Agent   string
	IsOwner bool
}

func SkillResource(id, ownerID string, facts SkillFacts) (authz.Resource, error) {
	return resource(authz.ResourceSkill, id, ownerID, map[string]string{
		"scope":    facts.Scope,
		"owner":    facts.Owner,
		"agent":    facts.Agent,
		"is_owner": boolString(facts.IsOwner),
	})
}

type VaultFacts struct {
	Scope   string
	Owner   string
	Agent   string
	IsOwner bool
}

func VaultResource(id, ownerID string, facts VaultFacts) (authz.Resource, error) {
	return resource(authz.ResourceVault, id, ownerID, map[string]string{
		"scope":    facts.Scope,
		"owner":    facts.Owner,
		"agent":    facts.Agent,
		"is_owner": boolString(facts.IsOwner),
	})
}

func ownerExecutorResource(typ authz.ResourceType, id, ownerID, owner, agent, state string, isOwner, isExecutor bool) (authz.Resource, error) {
	return resource(typ, id, ownerID, map[string]string{
		"owner":       owner,
		"agent":       agent,
		"state":       state,
		"is_owner":    boolString(isOwner),
		"is_executor": boolString(isExecutor),
	})
}

func resource(typ authz.ResourceType, id, ownerID string, attrs map[string]string) (authz.Resource, error) {
	return authz.NewResourceWithAttrs(typ, id, ownerID, attrs)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
