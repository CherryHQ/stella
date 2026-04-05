package channel

// IndexedAgent pairs an AgentInfo with its 1-based global index.
type IndexedAgent struct {
	AgentInfo
	GlobalIdx int
}

// IndexAgents wraps a full agent list with sequential 1-based indices.
func IndexAgents(agents []AgentInfo) []IndexedAgent {
	out := make([]IndexedAgent, len(agents))
	for i, a := range agents {
		out[i] = IndexedAgent{AgentInfo: a, GlobalIdx: i + 1}
	}
	return out
}
