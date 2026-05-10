package reflect

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
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

// countMutations inspects tool results (not calls) so that failed tool
// invocations are not counted as mutations.
func countMutations(messages []ai.Message) reviewResult {
	// First pass: collect tool call metadata keyed by call ID.
	type callMeta struct {
		toolName string
		action   string
	}
	calls := make(map[string]callMeta)
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
			calls[call.ID] = callMeta{toolName: call.Name, action: action}
		}
	}

	// Second pass: count successful tool results that correspond to mutating calls.
	var r reviewResult
	for _, msg := range messages {
		tr, ok := msg.(ai.ToolResultMessage)
		if !ok || tr.IsError {
			continue
		}
		meta, found := calls[tr.ToolCallID]
		if !found {
			continue
		}
		switch meta.toolName {
		case toolNameSkills:
			if meta.action == "create" || meta.action == "patch" {
				r.SkillsMutated++
			}
		case toolNameMemory:
			if meta.action == "profile_update" {
				r.MemoryUpdated = true
			}
		}
	}
	return r
}
