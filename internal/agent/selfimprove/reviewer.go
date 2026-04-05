package selfimprove

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
)

// Tool name constants used by the review agent.
const (
	toolNameSkills = "skills"
	toolNameMemory = "review_memory"
)

// ReviewResult holds the outcome of a single conversation review.
type ReviewResult struct {
	SkillsMutated int  // number of skills created or patched
	MemoryUpdated bool // whether user memory was updated
}

// Reviewer runs the review agent against a single conversation to extract
// skills and update user memory.
type Reviewer struct {
	runner *agent.Runner
}

// ReviewerConfig holds the tools and context needed to construct a Reviewer.
type ReviewerConfig struct {
	Providers      providers.ProviderGetter
	Model          ai.Model
	SkillsTool     tools.Tool // skills.SkillsTool or equivalent
	MemoryTool     tools.Tool // nil if memory review is not available
	ExistingSkills []string
}

// NewReviewer creates a Reviewer with skill extraction and optional memory review.
func NewReviewer(cfg ReviewerConfig) (*Reviewer, error) {
	reg := tools.NewRegistry()
	reg.Register(cfg.SkillsTool)
	if cfg.MemoryTool != nil {
		reg.Register(cfg.MemoryTool)
	}

	skillList := "None"
	if len(cfg.ExistingSkills) > 0 {
		skillList = strings.Join(cfg.ExistingSkills, ", ")
	}
	system := fmt.Sprintf(combinedReviewPrompt, skillList)

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Providers:       cfg.Providers,
		Model:           cfg.Model,
		Tools:           agent.ToolSetFromRegistry(reg),
		ToolDefinitions: reg.Definitions(),
	},
		agent.WithMaxTurns(5),
		agent.WithSystem(system),
	)
	if err != nil {
		return nil, fmt.Errorf("new reviewer: %w", err)
	}

	return &Reviewer{runner: runner}, nil
}

// Review runs the review agent on a conversation transcript.
func (r *Reviewer) Review(ctx context.Context, conversationText string) (ReviewResult, error) {
	history := []ai.Message{
		ai.UserMessage{Content: conversationText},
	}

	result, err := r.runner.Run(ctx, history, nil)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("review agent: %w", err)
	}

	return countMutations(result), nil
}

// countMutations counts skill mutations and memory updates in the result messages.
func countMutations(messages []ai.Message) ReviewResult {
	var r ReviewResult
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
			action, _ := call.Arguments["action"].(string)
			switch call.Name {
			case toolNameSkills:
				if action == "create" || action == "patch" {
					r.SkillsMutated++
				}
			case toolNameMemory:
				if action == "update" {
					r.MemoryUpdated = true
				}
			}
		}
	}
	return r
}
