package vault

// RunnerInvalidator is the runner-cache surface vault mutations need. It is
// implemented by the credentials service at the composition boundary.
type RunnerInvalidator interface {
	InvalidateAll() error
	InvalidateAgent(agentID string) error
	InvalidateUser(userID string) error
}

// InvalidateForScope closes the live runners affected by a vault mutation so
// the next message rebuilds sandbox env from the updated secret snapshot.
func InvalidateForScope(inv RunnerInvalidator, scope, userID, agentID string) error {
	if inv == nil {
		return nil
	}
	switch scope {
	case ScopeSystem:
		return inv.InvalidateAll()
	case ScopeSystemAgent:
		return inv.InvalidateAgent(agentID)
	default: // user, user_agent, and the empty default user scope
		if userID == "" {
			return nil
		}
		return inv.InvalidateUser(userID)
	}
}
