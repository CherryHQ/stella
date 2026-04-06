package channel

import (
	"strings"
	"testing"
)

func TestIndexAgents_Empty(t *testing.T) {
	got := IndexAgents(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

func TestIndexAgents_Indices(t *testing.T) {
	agents := []AgentInfo{
		{ID: "a1", Name: "Agent One"},
		{ID: "a2", Name: "Agent Two"},
	}
	indexed := IndexAgents(agents)
	if len(indexed) != 2 {
		t.Fatalf("expected 2, got %d", len(indexed))
	}
	if indexed[0].GlobalIdx != 1 || indexed[1].GlobalIdx != 2 {
		t.Errorf("unexpected indices: %d, %d", indexed[0].GlobalIdx, indexed[1].GlobalIdx)
	}
}

func TestFormatAgentList_Empty(t *testing.T) {
	got := FormatAgentList(nil, "")
	if !strings.Contains(got, "No agents") {
		t.Errorf("expected 'No agents', got %q", got)
	}
}

func TestFormatAgentList_WithCurrent(t *testing.T) {
	agents := []IndexedAgent{
		{AgentInfo: AgentInfo{ID: "a1", Name: "Agent One"}, GlobalIdx: 1},
		{AgentInfo: AgentInfo{ID: "a2", Name: "Agent Two"}, GlobalIdx: 2},
	}
	got := FormatAgentList(agents, "a1")
	if !strings.Contains(got, "✅") {
		t.Errorf("expected checkmark for current agent, got %q", got)
	}
	if !strings.Contains(got, "a1") || !strings.Contains(got, "a2") {
		t.Errorf("expected both agents in output, got %q", got)
	}
}
