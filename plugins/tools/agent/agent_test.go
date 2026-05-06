package agent

import (
	"testing"

	"github.com/vaayne/anna/pkg/ai"
)

// --- AgentConfig defaults ---

func TestAgentConfig_Defaults(t *testing.T) {
	cfg := AgentConfig{}
	if cfg.maxConcurrency() != defaultMaxConcurrency {
		t.Errorf("expected default maxConcurrency=%d, got %d", defaultMaxConcurrency, cfg.maxConcurrency())
	}
}

func TestAgentConfig_CustomValues(t *testing.T) {
	cfg := AgentConfig{MaxConcurrency: 1}
	if cfg.maxConcurrency() != 1 {
		t.Errorf("expected maxConcurrency=1, got %d", cfg.maxConcurrency())
	}
}

// --- parseAgentTasks ---

func TestParseAgentTasks_Basic(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "do something"},
		},
	}
	tasks, err := parseAgentTasks(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ID != "t1" || tasks[0].Task != "do something" {
		t.Errorf("unexpected task: %+v", tasks[0])
	}
}

func TestParseAgentTasks_AllFields(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{
				"id":     "t1",
				"task":   "write code",
				"preset": "worker",
				"model":  "claude-haiku",
			},
		},
	}
	tasks, err := parseAgentTasks(args)
	if err != nil {
		t.Fatal(err)
	}
	tc := tasks[0]
	if tc.Preset != "worker" {
		t.Errorf("expected preset 'worker', got %q", tc.Preset)
	}
	if tc.Model != "claude-haiku" {
		t.Errorf("expected model 'claude-haiku', got %q", tc.Model)
	}
}

func TestParseAgentTasks_AutoID(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"task": "first"},
			map[string]any{"task": "second"},
		},
	}
	tasks, err := parseAgentTasks(args)
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].ID != "task_0" {
		t.Errorf("expected auto id 'task_0', got %q", tasks[0].ID)
	}
	if tasks[1].ID != "task_1" {
		t.Errorf("expected auto id 'task_1', got %q", tasks[1].ID)
	}
}

func TestParseAgentTasks_MissingTasks(t *testing.T) {
	_, err := parseAgentTasks(map[string]any{})
	if err == nil {
		t.Error("expected error when tasks is missing")
	}
}

func TestParseAgentTasks_InvalidTasksType(t *testing.T) {
	_, err := parseAgentTasks(map[string]any{"tasks": "not an array"})
	if err == nil {
		t.Error("expected error when tasks is not an array")
	}
}

func TestParseAgentTasks_MissingTask(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1"},
		},
	}
	_, err := parseAgentTasks(args)
	if err == nil {
		t.Error("expected error when task is missing")
	}
}

func TestParseAgentTasks_DuplicateID(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "first"},
			map[string]any{"id": "t1", "task": "second"},
		},
	}
	_, err := parseAgentTasks(args)
	if err == nil {
		t.Error("expected error for duplicate task ID")
	}
}

// --- extractLastAssistant ---

func TestExtractLastAssistant_Found(t *testing.T) {
	history := []ai.Message{
		ai.UserMessage{Content: "hello"},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "world"}},
			StopReason: ai.StopReasonStop,
		},
	}
	text, reason := extractLastAssistant(history)
	if text != "world" {
		t.Errorf("expected 'world', got %q", text)
	}
	if reason != ai.StopReasonStop {
		t.Errorf("expected stop reason, got %q", reason)
	}
}

func TestExtractLastAssistant_NotFound(t *testing.T) {
	history := []ai.Message{
		ai.UserMessage{Content: "hello"},
	}
	text, reason := extractLastAssistant(history)
	if text != "" || reason != "" {
		t.Errorf("expected empty result, got %q %q", text, reason)
	}
}

func TestExtractLastAssistant_NoTextContent(t *testing.T) {
	// AssistantMessage with no TextContent block.
	history := []ai.Message{
		ai.AssistantMessage{Content: []ai.ContentBlock{}, StopReason: ai.StopReasonStop},
	}
	text, reason := extractLastAssistant(history)
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
	if reason != ai.StopReasonStop {
		t.Errorf("expected stop reason, got %q", reason)
	}
}

// --- AgentDefinition ---

func TestAgentDefinition_NoPresets(t *testing.T) {
	def := AgentDefinition(nil)
	if def.Name != agentToolName {
		t.Errorf("expected name %q, got %q", agentToolName, def.Name)
	}
}

func TestAgentDefinition_WithPresets(t *testing.T) {
	reg := &PresetRegistry{}
	// Registry with no presets should not panic.
	def := AgentDefinition(reg)
	if def.Name != agentToolName {
		t.Errorf("expected name %q, got %q", agentToolName, def.Name)
	}
}

// --- applyPreset ---

func TestApplyPreset_FillsDefaults(t *testing.T) {
	tc := agentTaskConfig{}
	preset := AgentPreset{
		Model:  "claude-haiku",
		System: "be helpful",
	}
	tc.applyPreset(preset)
	if tc.Model != "claude-haiku" {
		t.Errorf("expected model 'claude-haiku', got %q", tc.Model)
	}
	if tc.System != "be helpful" {
		t.Errorf("expected system prompt, got %q", tc.System)
	}
}

func TestApplyPreset_DoesNotOverrideExisting(t *testing.T) {
	tc := agentTaskConfig{Model: "gpt-4", System: "existing"}
	preset := AgentPreset{Model: "claude-haiku", System: "preset system"}
	tc.applyPreset(preset)
	if tc.Model != "gpt-4" {
		t.Error("preset should not override explicit model")
	}
	if tc.System != "existing" {
		t.Error("preset should not override explicit system")
	}
}
