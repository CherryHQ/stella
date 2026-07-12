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

// WorkspaceRequest builds an action against the workspace rooted by sessionID.
func WorkspaceRequest(action authz.Action, sessionID, ownerID string, facts SessionFacts) (authz.Request, error) {
	res, err := WorkspaceResource(sessionID, ownerID, facts)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}
