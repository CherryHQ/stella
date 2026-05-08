package reflect

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestCountMutations_SkipsFailedToolCalls(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "tc1", Name: toolNameSkills, Arguments: map[string]any{"action": "create"}},
			ai.ToolCall{ID: "tc2", Name: toolNameMemory, Arguments: map[string]any{"action": "profile_update"}},
			ai.ToolCall{ID: "tc3", Name: toolNameSkills, Arguments: map[string]any{"action": "create"}},
		}},
		// tc1 succeeds
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		// tc2 fails
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: true},
		// tc3 succeeds
		ai.ToolResultMessage{ToolCallID: "tc3", ToolName: toolNameSkills, IsError: false},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 2 {
		t.Errorf("expected 2 skills mutated, got %d", r.SkillsMutated)
	}
	if r.MemoryUpdated {
		t.Error("expected MemoryUpdated=false since the tool call failed")
	}
}

func TestCountMutations_CountsSuccessfulCalls(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "tc1", Name: toolNameSkills, Arguments: map[string]any{"action": "create"}},
			ai.ToolCall{ID: "tc2", Name: toolNameMemory, Arguments: map[string]any{"action": "profile_update"}},
		}},
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: false},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 1 {
		t.Errorf("expected 1 skill mutated, got %d", r.SkillsMutated)
	}
	if !r.MemoryUpdated {
		t.Error("expected MemoryUpdated=true")
	}
}

func TestCountMutations_IgnoresNonMutatingActions(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "tc1", Name: toolNameSkills, Arguments: map[string]any{"action": "list"}},
			ai.ToolCall{ID: "tc2", Name: toolNameMemory, Arguments: map[string]any{"action": "profile_get"}},
		}},
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: false},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 0 {
		t.Errorf("expected 0 skills mutated, got %d", r.SkillsMutated)
	}
	if r.MemoryUpdated {
		t.Error("expected MemoryUpdated=false for non-mutating actions")
	}
}

func TestCountMutations_Empty(t *testing.T) {
	r := countMutations(nil)
	if r.SkillsMutated != 0 || r.MemoryUpdated {
		t.Error("expected zero result for nil messages")
	}
}
