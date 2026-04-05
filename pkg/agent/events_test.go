package agent

import "testing"

func TestLoopEventKinds(t *testing.T) {
	tests := []struct {
		name string
		ev   LoopEvent
		want string
	}{
		{"AgentStarted", AgentStarted{}, "agentStarted"},
		{"AssistantStarted", AssistantStarted{}, "assistantStarted"},
		{"AssistantDelta", AssistantDelta{}, "assistantDelta"},
		{"AssistantFinished", AssistantFinished{}, "assistantFinished"},
		{"TurnStarted", TurnStarted{}, "turnStarted"},
		{"TurnFinished", TurnFinished{}, "turnFinished"},
		{"ToolStarted", ToolStarted{}, "toolStarted"},
		{"ToolFinished", ToolFinished{}, "toolFinished"},
		{"AgentFinished", AgentFinished{}, "agentFinished"},
		{"AgentErrored", AgentErrored{}, "agentErrored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.Kind(); got != tt.want {
				t.Errorf("Kind() = %q, want %q", got, tt.want)
			}
		})
	}
}
