package reflect

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
)

const (
	toolNameSkills = "skills"
	toolNameMemory = "memory"
)

// reviewResult holds the outcome of a single conversation review.
type reviewResult struct {
	SkillsMutated int  // number of skills created or patched
	MemoryUpdated bool // whether user memory was updated
}

// reviewerConfig holds the tools and context needed to construct a reviewer.
type reviewerConfig struct {
	Providers      providers.ProviderGetter
	Model          ai.Model
	SkillsTool     tools.Tool
	MemoryTool     tools.Tool // nil if memory review is not available
	ExistingSkills []string
}

// reviewer runs the review agent against a single conversation.
type reviewer struct {
	runner *agent.Runner
}

func newReviewer(cfg reviewerConfig) (*reviewer, error) {
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

	r, err := agent.NewRunner(agent.RunnerConfig{
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

	return &reviewer{runner: r}, nil
}

func (r *reviewer) review(ctx context.Context, conversationText string) (reviewResult, error) {
	history := []ai.Message{
		ai.UserMessage{Content: conversationText},
	}

	result, err := r.runner.Run(ctx, history, nil)
	if err != nil {
		return reviewResult{}, fmt.Errorf("review agent: %w", err)
	}

	return countMutations(result), nil
}

func countMutations(messages []ai.Message) reviewResult {
	var r reviewResult
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
				if action == "profile_update" {
					r.MemoryUpdated = true
				}
			}
		}
	}
	return r
}
