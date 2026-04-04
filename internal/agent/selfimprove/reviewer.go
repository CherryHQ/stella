package selfimprove

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

// Reviewer runs the review agent against a single conversation to extract skills.
type Reviewer struct {
	providers ai.ProviderGetter
	model     ai.Model
	toolDef   toolspec.Definition
	toolFunc  engine.ToolFunc
	system    string
}

// NewReviewer creates a Reviewer that uses the given provider and model.
// targetDir is the writable skills directory.
// existingSkills lists skill names already present (to avoid duplicates).
func NewReviewer(providers ai.ProviderGetter, model ai.Model, targetDir string, existingSkills []string) *Reviewer {
	tool := NewReviewSkillsTool(targetDir)
	def := tool.Definition()

	toolFunc := func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
		args := call.Arguments
		if args == nil {
			args = make(map[string]any)
		}
		result, err := tool.Execute(ctx, args)
		if err != nil {
			return ai.TextContent{Text: fmt.Sprintf("error: %v", err)}, nil
		}
		return ai.TextContent{Text: result}, nil
	}

	skillList := "None"
	if len(existingSkills) > 0 {
		skillList = strings.Join(existingSkills, ", ")
	}
	system := fmt.Sprintf(reviewSystemPrompt, skillList)

	return &Reviewer{
		providers: providers,
		model:     model,
		toolDef:   def,
		toolFunc:  toolFunc,
		system:    system,
	}
}

// Review runs the review agent on a conversation transcript.
// Returns the number of skills created or patched.
func (r *Reviewer) Review(ctx context.Context, conversationText string) (int, error) {
	eng := &engine.Engine{Providers: r.providers}

	cfg := engine.LoopConfig{
		Model:           r.model,
		MaxTurns:        5,
		Tools:           engine.ToolSet{r.toolDef.Name: r.toolFunc},
		ToolDefinitions: []toolspec.Definition{r.toolDef},
		System:          r.system,
	}

	history := []ai.Message{
		ai.UserMessage{Content: conversationText},
	}

	result, err := eng.Run(ctx, cfg, history, nil)
	if err != nil {
		return 0, fmt.Errorf("review engine: %w", err)
	}

	return countSkillMutations(result), nil
}

// countSkillMutations counts tool calls that created or patched skills.
func countSkillMutations(messages []ai.Message) int {
	count := 0
	for _, msg := range messages {
		aMsg, ok := msg.(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range aMsg.Content {
			call, ok := block.(ai.ToolCall)
			if !ok {
				continue
			}
			if call.Name != "review_skills" {
				continue
			}
			action, _ := call.Arguments["action"].(string)
			if action == "create" || action == "patch" {
				count++
			}
		}
	}
	return count
}
