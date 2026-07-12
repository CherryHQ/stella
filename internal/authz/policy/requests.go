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
