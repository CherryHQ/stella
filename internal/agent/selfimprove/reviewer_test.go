package selfimprove

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/ai"
)

// mockProviderGetter satisfies ai.ProviderGetter for construction tests.
type mockProviderGetter struct{}

func (m *mockProviderGetter) Get(api string) (ai.ProviderAdapter, bool) {
	return nil, false
}

func TestNewReviewer(t *testing.T) {
	dir := t.TempDir()
	providers := &mockProviderGetter{}
	model := ai.Model{ID: "test-model", Name: "test", API: "openai"}

	r := NewReviewer(providers, model, dir, nil)
	if r == nil {
		t.Fatal("NewReviewer returned nil")
	}
	if r.providers != providers {
		t.Error("providers not set")
	}
	if r.model.ID != "test-model" {
		t.Errorf("model = %q, want %q", r.model.ID, "test-model")
	}
}

func TestNewReviewerWithExistingSkills(t *testing.T) {
	dir := t.TempDir()
	providers := &mockProviderGetter{}
	model := ai.Model{ID: "test-model", Name: "test", API: "openai"}

	existing := []string{"deploy-to-staging", "fix-flaky-tests"}
	r := NewReviewer(providers, model, dir, existing)

	if !strings.Contains(r.system, "deploy-to-staging") {
		t.Error("system prompt should list existing skill names")
	}
	if !strings.Contains(r.system, "fix-flaky-tests") {
		t.Error("system prompt should list existing skill names")
	}
}

func TestNewReviewerNoExistingSkills(t *testing.T) {
	dir := t.TempDir()
	providers := &mockProviderGetter{}
	model := ai.Model{ID: "test-model", Name: "test", API: "openai"}

	r := NewReviewer(providers, model, dir, nil)
	if !strings.Contains(r.system, "None") {
		t.Error("system prompt should say 'None' when no existing skills")
	}
}

func TestReviewerToolDefinition(t *testing.T) {
	dir := t.TempDir()
	providers := &mockProviderGetter{}
	model := ai.Model{ID: "test-model", Name: "test", API: "openai"}

	r := NewReviewer(providers, model, dir, nil)
	if r.toolDef.Name != "review_skills" {
		t.Errorf("tool name = %q, want %q", r.toolDef.Name, "review_skills")
	}
}

func TestReviewSystemPromptContent(t *testing.T) {
	checks := []string{
		"skill extraction agent",
		"procedural knowledge",
		"Nothing to save.",
		"lowercase-hyphenated",
		"review_skills",
	}
	for _, check := range checks {
		if !strings.Contains(reviewSystemPrompt, check) {
			t.Errorf("system prompt missing phrase: %q", check)
		}
	}
}

func TestCountSkillMutations(t *testing.T) {
	tests := []struct {
		name     string
		messages []ai.Message
		want     int
	}{
		{
			name:     "no messages",
			messages: nil,
			want:     0,
		},
		{
			name: "create and patch counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      "review_skills",
							Arguments: map[string]any{"action": "create", "name": "a"},
						},
						ai.ToolCall{
							Name:      "review_skills",
							Arguments: map[string]any{"action": "patch", "name": "b"},
						},
					},
				},
			},
			want: 2,
		},
		{
			name: "deprecate not counted",
			messages: []ai.Message{
				ai.AssistantMessage{
					Content: []ai.ContentBlock{
						ai.ToolCall{
							Name:      "review_skills",
							Arguments: map[string]any{"action": "deprecate", "name": "c"},
						},
					},
				},
			},
			want: 0,
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
			want: 0,
		},
		{
			name: "user messages ignored",
			messages: []ai.Message{
				ai.UserMessage{Content: "hello"},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countSkillMutations(tt.messages)
			if got != tt.want {
				t.Errorf("countSkillMutations = %d, want %d", got, tt.want)
			}
		})
	}
}
