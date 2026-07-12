package policy

import "github.com/CherryHQ/stella/internal/authz"

// Typed agent request builders are the only sanctioned way to turn loaded
// agent facts into an authz.Request. Keeping the complete fact set in every
// request makes custom-policy predicates total rather than accidentally absent.

func AgentReadRequest(agentID, ownerID string, facts AgentFacts) (authz.Request, error) {
	return agentActionRequest(authz.ActionRead, agentID, ownerID, facts)
}

func AgentUseRequest(agentID, ownerID string, facts AgentFacts) (authz.Request, error) {
	return agentActionRequest(authz.ActionExecute, agentID, ownerID, facts)
}

func AgentManageRequest(agentID, ownerID string, facts AgentFacts) (authz.Request, error) {
	return agentActionRequest(authz.ActionManage, agentID, ownerID, facts)
}

func AgentDeleteRequest(agentID, ownerID string, facts AgentFacts) (authz.Request, error) {
	return agentActionRequest(authz.ActionDelete, agentID, ownerID, facts)
}

// AgentListRequest builds the collection-level agent list request. Per-agent
// visibility is decided separately using AgentReadRequest in the same evaluation.
func AgentListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceAgent, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

func AgentCreateRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceAgent, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionCreate, res, authz.InvocationFacts{})
}

func agentActionRequest(action authz.Action, agentID, ownerID string, facts AgentFacts) (authz.Request, error) {
	res, err := AgentResource(agentID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// SessionRequest builds an action against one durable Session.
func SessionRequest(action authz.Action, sessionID, ownerID string, facts SessionFacts) (authz.Request, error) {
	res, err := SessionResource(sessionID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

func SessionReadRequest(sessionID, ownerID string, facts SessionFacts) (authz.Request, error) {
	return SessionRequest(authz.ActionRead, sessionID, ownerID, facts)
}

// SessionCreateRequest uses the durable owner as the resource ID because a
// generated session ID does not exist until after authorization.
func SessionCreateRequest(ownerID string, facts SessionFacts) (authz.Request, error) {
	return SessionRequest(authz.ActionCreate, ownerID, ownerID, facts)
}

func SessionListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceSession, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// WorkflowRequest builds an action against one durable Workflow.
func WorkflowRequest(action authz.Action, workflowID, ownerID string, facts WorkflowFacts) (authz.Request, error) {
	res, err := WorkflowResource(workflowID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// WorkflowListRequest builds the collection-level workflow list request. Per-row
// visibility is decided separately with a WorkflowRequest read in the same
// evaluation.
func WorkflowListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceWorkflow, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// GoalRequest builds an action against one durable Goal.
func GoalRequest(action authz.Action, goalID, ownerID string, facts GoalFacts) (authz.Request, error) {
	res, err := GoalResource(goalID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// GoalListRequest builds the collection-level goal list request. Per-row
// visibility is decided separately with a GoalRequest read in the same
// evaluation.
func GoalListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceGoal, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// SchedulerRequest builds an action against one durable Scheduler job.
func SchedulerRequest(action authz.Action, jobID, ownerID string, facts SchedulerFacts) (authz.Request, error) {
	res, err := SchedulerResource(jobID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// SchedulerListRequest builds the collection-level scheduler list request. Per-row
// visibility is decided separately with a SchedulerRequest read in the same
// evaluation.
func SchedulerListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceScheduler, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// SkillRequest builds an action against one durable Skill.
func SkillRequest(action authz.Action, skillID, ownerID string, facts SkillFacts) (authz.Request, error) {
	res, err := SkillResource(skillID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// SkillListRequest builds the collection-level skill list request. Per-scope
// visibility is decided separately with a SkillRequest in the same evaluation.
func SkillListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceSkill, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// VaultRequest builds an action against one durable Vault entry (or a scope
// bucket for create/list, where the id is the effective owner key).
func VaultRequest(action authz.Action, id, ownerID string, facts VaultFacts) (authz.Request, error) {
	res, err := VaultResource(id, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// ConnectionRequest builds an action against one user OAuth connection.
func ConnectionRequest(action authz.Action, id, ownerID string, facts ConnectionFacts) (authz.Request, error) {
	res, err := ConnectionResource(id, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

// ConnectionListRequest builds the collection-level connection list request.
func ConnectionListRequest() (authz.Request, error) {
	return listRequest(authz.ResourceConnection)
}

// EmailRequest builds an action against the caller's email resource.
func EmailRequest(action authz.Action, id, ownerID string, facts OwnedFacts) (authz.Request, error) {
	return ownedRequest(authz.ResourceEmail, action, id, ownerID, facts)
}

// EmailListRequest builds the collection-level email list request.
func EmailListRequest() (authz.Request, error) { return listRequest(authz.ResourceEmail) }

// ShareRequest builds an action against one durable Share.
func ShareRequest(action authz.Action, id, ownerID string, facts OwnedFacts) (authz.Request, error) {
	return ownedRequest(authz.ResourceShare, action, id, ownerID, facts)
}

// ShareListRequest builds the collection-level share list request. Per-row
// visibility is decided separately with a ShareRequest read in the same eval.
func ShareListRequest() (authz.Request, error) { return listRequest(authz.ResourceShare) }

// RecallyRequest builds an action against the caller's recally library.
func RecallyRequest(action authz.Action, id, ownerID string, facts OwnedFacts) (authz.Request, error) {
	return ownedRequest(authz.ResourceRecally, action, id, ownerID, facts)
}

// RecallyListRequest builds the collection-level recally list request.
func RecallyListRequest() (authz.Request, error) { return listRequest(authz.ResourceRecally) }

func ownedRequest(rt authz.ResourceType, action authz.Action, id, ownerID string, facts OwnedFacts) (authz.Request, error) {
	res, err := ownedResource(rt, id, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}

func listRequest(rt authz.ResourceType) (authz.Request, error) {
	res, err := authz.NewResource(rt, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

// WorkspaceRequest builds an action against the workspace rooted by sessionID.
func WorkspaceRequest(action authz.Action, sessionID, ownerID string, facts SessionFacts) (authz.Request, error) {
	res, err := WorkspaceResource(sessionID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}
