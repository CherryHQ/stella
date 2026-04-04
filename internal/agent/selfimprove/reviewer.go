package selfimprove

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
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

// reviewTool is the interface shared by the skills tool and memory tool.
type reviewTool interface {
	Definition() toolspec.Definition
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Reviewer runs the review agent against a single conversation to extract
// skills and update user memory.
type Reviewer struct {
	providers       ai.ProviderGetter
	model           ai.Model
	tools           engine.ToolSet
	toolDefinitions []toolspec.Definition
	system          string
}

// ReviewerConfig holds the tools and context needed to construct a Reviewer.
type ReviewerConfig struct {
	Providers      ai.ProviderGetter
	Model          ai.Model
	SkillsTool     reviewTool // skills.SkillsTool or equivalent
	MemoryTool     reviewTool // nil if memory review is not available
	ExistingSkills []string
}

// NewReviewer creates a Reviewer with skill extraction and optional memory review.
func NewReviewer(cfg ReviewerConfig) *Reviewer {
	tools := engine.ToolSet{}
	var defs []toolspec.Definition

	// Skills tool (always present).
	skillsDef := cfg.SkillsTool.Definition()
	tools[skillsDef.Name] = wrapTool(cfg.SkillsTool)
	defs = append(defs, skillsDef)

	// Memory tool (optional).
	if cfg.MemoryTool != nil {
		memDef := cfg.MemoryTool.Definition()
		tools[memDef.Name] = wrapTool(cfg.MemoryTool)
		defs = append(defs, memDef)
	}

	skillList := "None"
	if len(cfg.ExistingSkills) > 0 {
		skillList = strings.Join(cfg.ExistingSkills, ", ")
	}
	system := fmt.Sprintf(combinedReviewPrompt, skillList)

	return &Reviewer{
		providers:       cfg.Providers,
		model:           cfg.Model,
		tools:           tools,
		toolDefinitions: defs,
		system:          system,
	}
}

func wrapTool(t reviewTool) engine.ToolFunc {
	return func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
		args := call.Arguments
		if args == nil {
			args = make(map[string]any)
		}
		result, err := t.Execute(ctx, args)
		if err != nil {
			return ai.TextContent{Text: fmt.Sprintf("error: %v", err)}, nil
		}
		return ai.TextContent{Text: result}, nil
	}
}

// Review runs the review agent on a conversation transcript.
func (r *Reviewer) Review(ctx context.Context, conversationText string) (ReviewResult, error) {
	eng := &engine.Engine{Providers: r.providers}

	cfg := engine.LoopConfig{
		Model:           r.model,
		MaxTurns:        5,
		Tools:           r.tools,
		ToolDefinitions: r.toolDefinitions,
		System:          r.system,
	}

	history := []ai.Message{
		ai.UserMessage{Content: conversationText},
	}

	result, err := eng.Run(ctx, cfg, history, nil)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("review engine: %w", err)
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
