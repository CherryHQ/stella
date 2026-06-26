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
			ai.ToolCall{ID: "tc4", Name: toolNameKnowledge, Arguments: map[string]any{"action": "patch"}},
		}},
		// tc1 succeeds
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		// tc2 fails
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: true},
		// tc3 succeeds
		ai.ToolResultMessage{ToolCallID: "tc3", ToolName: toolNameSkills, IsError: false},
		// tc4 fails
		ai.ToolResultMessage{ToolCallID: "tc4", ToolName: toolNameKnowledge, IsError: true},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 2 {
		t.Errorf("expected 2 skills mutated, got %d", r.SkillsMutated)
	}
	if r.MemoryUpdated {
		t.Error("expected MemoryUpdated=false since the tool call failed")
	}
	if r.KnowledgeMutated != 0 {
		t.Errorf("expected 0 knowledge mutations since the tool call failed, got %d", r.KnowledgeMutated)
	}
}

func TestCountMutations_CountsSuccessfulCalls(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "tc1", Name: toolNameSkills, Arguments: map[string]any{"action": "create"}},
			ai.ToolCall{ID: "tc2", Name: toolNameMemory, Arguments: map[string]any{"action": "profile_update"}},
			ai.ToolCall{ID: "tc3", Name: toolNameKnowledge, Arguments: map[string]any{"action": "create"}},
			ai.ToolCall{ID: "tc4", Name: toolNameKnowledge, Arguments: map[string]any{"action": "patch"}},
			ai.ToolCall{ID: "tc5", Name: toolNameKnowledge, Arguments: map[string]any{"action": "deprecate"}},
		}},
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc3", ToolName: toolNameKnowledge, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc4", ToolName: toolNameKnowledge, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc5", ToolName: toolNameKnowledge, IsError: false},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 1 {
		t.Errorf("expected 1 skill mutated, got %d", r.SkillsMutated)
	}
	if !r.MemoryUpdated {
		t.Error("expected MemoryUpdated=true")
	}
	if r.KnowledgeMutated != 3 {
		t.Errorf("expected 3 knowledge mutations, got %d", r.KnowledgeMutated)
	}
}

func TestCountMutations_IgnoresNonMutatingActions(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "tc1", Name: toolNameSkills, Arguments: map[string]any{"action": "list"}},
			ai.ToolCall{ID: "tc2", Name: toolNameMemory, Arguments: map[string]any{"action": "profile_get"}},
			ai.ToolCall{ID: "tc3", Name: toolNameKnowledge, Arguments: map[string]any{"action": "list"}},
		}},
		ai.ToolResultMessage{ToolCallID: "tc1", ToolName: toolNameSkills, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc2", ToolName: toolNameMemory, IsError: false},
		ai.ToolResultMessage{ToolCallID: "tc3", ToolName: toolNameKnowledge, IsError: false},
	}

	r := countMutations(messages)
	if r.SkillsMutated != 0 {
		t.Errorf("expected 0 skills mutated, got %d", r.SkillsMutated)
	}
	if r.MemoryUpdated {
		t.Error("expected MemoryUpdated=false for non-mutating actions")
	}
	if r.KnowledgeMutated != 0 {
		t.Errorf("expected 0 knowledge mutations, got %d", r.KnowledgeMutated)
	}
}

func TestCountMutations_Empty(t *testing.T) {
	r := countMutations(nil)
	if r.SkillsMutated != 0 || r.KnowledgeMutated != 0 || r.MemoryUpdated {
		t.Error("expected zero result for nil messages")
	}
}
