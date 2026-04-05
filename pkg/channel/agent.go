package channel

import (
	"fmt"
	"strings"
)

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

// FormatAgentList builds a text-based agent list, marking the current agent.
func FormatAgentList(agents []IndexedAgent, currentAgentID string) string {
	if len(agents) == 0 {
		return "No agents available."
	}
	var sb strings.Builder
	sb.WriteString("Available agents:\n\n")
	for _, ag := range agents {
		prefix := "• "
		if ag.ID == currentAgentID {
			prefix = "✅ "
		}
		fmt.Fprintf(&sb, "%s%s (%s)\n", prefix, ag.ID, ag.Name)
	}
	sb.WriteString("\nUse /agent <slug> to switch.")
	return sb.String()
}
