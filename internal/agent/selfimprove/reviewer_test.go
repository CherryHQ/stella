package selfimprove

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/skills"
)

// mockProviderGetter satisfies ai.ProviderGetter for construction tests.
type mockProviderGetter struct{}

func (m *mockProviderGetter) Get(api string) (ai.ProviderAdapter, bool) {
	return nil, false
}

func newTestReviewerConfig(dir string) ReviewerConfig {
	return ReviewerConfig{
		Providers:  &mockProviderGetter{},
		Model:      ai.Model{ID: "test-model", Name: "test", API: "openai"},
		SkillsTool: skills.NewTool("", dir, "", 0),
	}
}

func TestNewReviewer(t *testing.T) {
	t.Parallel()
	cfg := newTestReviewerConfig(t.TempDir())
	r := NewReviewer(cfg)
	if r == nil {
		t.Fatal("NewReviewer returned nil")
	}
	if r.providers != cfg.Providers {
		t.Error("providers not set")
	}
	if r.model.ID != "test-model" {
		t.Errorf("model = %q, want %q", r.model.ID, "test-model")
	}
}

func TestNewReviewerWithExistingSkills(t *testing.T) {
	t.Parallel()
	cfg := newTestReviewerConfig(t.TempDir())
	cfg.ExistingSkills = []string{"deploy-to-staging", "fix-flaky-tests"}
	r := NewReviewer(cfg)

	if !strings.Contains(r.system, "deploy-to-staging") {
		t.Error("system prompt should list existing skill names")
	}
	if !strings.Contains(r.system, "fix-flaky-tests") {
		t.Error("system prompt should list existing skill names")
	}
}

func TestNewReviewerNoExistingSkills(t *testing.T) {
	t.Parallel()
	cfg := newTestReviewerConfig(t.TempDir())
	r := NewReviewer(cfg)
	if !strings.Contains(r.system, "None") {
		t.Error("system prompt should say 'None' when no existing skills")
	}
}

func TestReviewerToolDefinitions(t *testing.T) {
	t.Parallel()

	t.Run("skills only", func(t *testing.T) {
		t.Parallel()
		cfg := newTestReviewerConfig(t.TempDir())
		r := NewReviewer(cfg)
		if len(r.toolDefinitions) != 1 {
			t.Fatalf("tool definitions = %d, want 1", len(r.toolDefinitions))
		}
		if r.toolDefinitions[0].Name != toolNameSkills {
			t.Errorf("tool name = %q, want %q", r.toolDefinitions[0].Name, toolNameSkills)
		}
	})

	t.Run("skills and memory", func(t *testing.T) {
		t.Parallel()
		cfg := newTestReviewerConfig(t.TempDir())
		cfg.MemoryTool = NewReviewMemoryTool(nil, 1, "agent-1")
		r := NewReviewer(cfg)
		if len(r.toolDefinitions) != 2 {
			t.Fatalf("tool definitions = %d, want 2", len(r.toolDefinitions))
		}
		names := map[string]bool{}
		for _, d := range r.toolDefinitions {
			names[d.Name] = true
		}
		if !names[toolNameSkills] {
			t.Error("missing skills tool")
		}
		if !names[toolNameMemory] {
			t.Error("missing review_memory tool")
		}
	})
}

func TestReviewSystemPromptContent(t *testing.T) {
	t.Parallel()
	checks := []string{
		"self-improvement agent",
		"Memory",
		"Skills",
		"Nothing to save.",
		"lowercase-hyphenated",
		"skills tool",
		"review_memory",
	}
	for _, check := range checks {
		if !strings.Contains(combinedReviewPrompt, check) {
			t.Errorf("combined prompt missing phrase: %q", check)
		}
	}
}

func TestCountMutations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		messages []ai.Message
		want     ReviewResult
	}{
		{
			name:     "no messages",
			messages: nil,
			want:     ReviewResult{},
		},
		{
			name: "skill create and patch counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      toolNameSkills,
							Arguments: map[string]any{"action": "create", "name": "a"},
						},
						ai.ToolCall{
							Name:      toolNameSkills,
							Arguments: map[string]any{"action": "patch", "name": "b"},
						},
					},
				},
			},
			want: ReviewResult{SkillsMutated: 2},
		},
		{
			name: "deprecate not counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      toolNameSkills,
							Arguments: map[string]any{"action": "deprecate", "name": "c"},
						},
					},
				},
			},
			want: ReviewResult{},
		},
		{
			name: "memory update counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      toolNameMemory,
							Arguments: map[string]any{"action": "update", "content": "user likes Go"},
						},
					},
				},
			},
			want: ReviewResult{MemoryUpdated: true},
		},
		{
			name: "memory get not counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      toolNameMemory,
							Arguments: map[string]any{"action": "get"},
						},
					},
				},
			},
			want: ReviewResult{},
		},
		{
			name: "mixed skills and memory",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      toolNameSkills,
							Arguments: map[string]any{"action": "create", "name": "a"},
						},
						ai.ToolCall{
							Name:      toolNameMemory,
							Arguments: map[string]any{"action": "update", "content": "notes"},
						},
					},
				},
			},
			want: ReviewResult{SkillsMutated: 1, MemoryUpdated: true},
		},
		{
			name: "other tool not counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      "other_tool",
							Arguments: map[string]any{"action": "create"},
						},
					},
				},
			},
			want: ReviewResult{},
		},
		{
			name: "user messages ignored",
			messages: []ai.Message{
				ai.UserMessage{Content: "hello"},
			},
			want: ReviewResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := countMutations(tt.messages)
			if got != tt.want {
				t.Errorf("countMutations = %+v, want %+v", got, tt.want)
			}
		})
	}
}
