package selfimprove

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
)

// mockProviderGetter satisfies providers.ProviderGetter for construction tests.
type mockProviderGetter struct{}

func (m *mockProviderGetter) Get(api string) (providers.ProviderAdapter, bool) {
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
	r, err := NewReviewer(cfg)
	if err != nil {
		t.Fatalf("NewReviewer: %v", err)
	}
	if r == nil {
		t.Fatal("NewReviewer returned nil")
	}
	if r.runner == nil {
		t.Error("runner not set")
	}
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
		"memory tool",
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
							Arguments: map[string]any{"action": "profile_update", "content": "user likes Go"},
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
							Arguments: map[string]any{"action": "profile_get"},
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
							Arguments: map[string]any{"action": "profile_update", "content": "notes"},
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
