package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

const (
	delegateToolName      = "delegate"
	defaultMaxTurns       = 10
	defaultTimeoutSeconds = 120
	maxResultChars        = 16000 // ~4096 tokens
)

// DelegateConfig holds the dependencies needed to spawn subagent loops.
type DelegateConfig struct {
	Engine      *engine.Engine
	Registry    *Registry
	Model       ai.Model
	APIKey      string
	System      string
	PluginHooks engine.PluginHookRunner // optional plugin lifecycle hooks
}

// DelegateTool spawns child agent loops for bounded subtasks.
type DelegateTool struct {
	cfg DelegateConfig
}

// NewDelegateTool creates a delegate tool with the given configuration.
func NewDelegateTool(cfg DelegateConfig) *DelegateTool {
	return &DelegateTool{cfg: cfg}
}

func (t *DelegateTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        delegateToolName,
		Description: "Delegate one or more tasks to subagents with isolated context. Multiple tasks run in parallel. Use for focused subtasks like research, code review, or drafting that benefit from fresh context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "One or more subtasks to delegate. Multiple tasks run in parallel.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":   map[string]any{"type": "string", "description": "Task identifier for result mapping."},
							"task": map[string]any{"type": "string", "description": "Task description for the subagent."},
							"model": map[string]any{
								"type":        "string",
								"description": "Optional model override (e.g. 'claude-haiku-4-5-20251001'). Defaults to parent model.",
							},
							"system": map[string]any{
								"type":        "string",
								"description": "Optional additional system instructions appended to the base prompt.",
							},
							"tools": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "string"},
								"description": "Optional tool whitelist. Only these tools will be available. Defaults to all parent tools minus delegate.",
							},
							"max_turns": map[string]any{
								"type":        "integer",
								"description": "Max agent loop turns. Defaults to 10.",
							},
							"timeout_seconds": map[string]any{
								"type":        "integer",
								"description": "Per-task timeout in seconds. Defaults to 120.",
							},
						},
						"required": []string{"id", "task"},
					},
				},
			},
			"required": []string{"tasks"},
		},
	}
}

func (t *DelegateTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	tasks, err := parseDelegateTasks(args)
	if err != nil {
		return "", fmt.Errorf("delegate: %w", err)
	}
	if len(tasks) == 0 {
		return "", fmt.Errorf("delegate: at least one task is required")
	}

	results := make(map[string]taskResult, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, task := range tasks {
		wg.Add(1)
		go func(tc delegateTaskConfig) {
			defer wg.Done()
			result := t.runSubAgent(ctx, tc)
			mu.Lock()
			results[tc.ID] = result
			mu.Unlock()
		}(task)
	}
	wg.Wait()

	envelope := map[string]any{"results": results}
	out, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("delegate: marshal results: %w", err)
	}
	return string(out), nil
}

type delegateTaskConfig struct {
	ID             string
	Task           string
	Model          string
	System         string
	Tools          []string // nil = all tools; empty = no tools
	HasTools       bool     // true when "tools" key was present in args
	MaxTurns       int
	TimeoutSeconds int
}

type taskResult struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func (t *DelegateTool) runSubAgent(parentCtx context.Context, tc delegateTaskConfig) (result taskResult) {
	log := slog.With("component", "delegate", "task_id", tc.ID)

	defer func() {
		if r := recover(); r != nil {
			result = taskResult{Error: fmt.Sprintf("panic: %v", r)}
			log.Error("subagent panicked", "error", r)
		}
	}()

	timeout := time.Duration(defaultTimeoutSeconds) * time.Second
	if tc.TimeoutSeconds > 0 {
		timeout = time.Duration(tc.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	// Build scoped tool set.
	toolSet, toolDefs, err := t.buildScopedTools(tc.Tools, tc.HasTools)
	if err != nil {
		return taskResult{Error: fmt.Sprintf("invalid tools: %v", err)}
	}

	// Build system prompt: base + optional additions.
	system := t.cfg.System
	if tc.System != "" {
		system += "\n\n" + tc.System
	}

	// Resolve model: override name if specified, keep same API.
	model := t.cfg.Model
	if tc.Model != "" {
		model.Name = tc.Model
		model.ID = tc.Model
	}

	maxTurns := defaultMaxTurns
	if tc.MaxTurns > 0 {
		maxTurns = tc.MaxTurns
	}

	cfg := engine.LoopConfig{
		Model:           model,
		StreamOptions:   ai.StreamOptions{APIKey: t.cfg.APIKey},
		MaxTurns:        maxTurns,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
		System:          system,
		PluginHooks:     t.cfg.PluginHooks,
	}

	messages := []ai.Message{
		ai.UserMessage{Content: tc.Task},
	}

	log.Info("subagent started")
	start := time.Now()

	history, err := t.cfg.Engine.Run(ctx, cfg, messages, nil)
	duration := time.Since(start)

	if err != nil {
		log.Error("subagent failed", "duration", duration, "error", err)
		return taskResult{Error: err.Error()}
	}

	log.Info("subagent finished", "duration", duration)

	output := extractLastAssistantText(history)
	output = truncateResult(output, maxResultChars)
	return taskResult{Output: output}
}

// buildScopedTools creates a filtered tool set for the child agent.
// It always excludes "delegate" to prevent recursion.
// If hasWhitelist is true, only the listed tools are included (empty list = no tools).
// If hasWhitelist is false, all parent tools (minus delegate) are available.
func (t *DelegateTool) buildScopedTools(whitelist []string, hasWhitelist bool) (engine.ToolSet, []toolspec.Definition, error) {
	allDefs := t.cfg.Registry.Definitions()

	// Build allowed set from whitelist, if provided.
	var allowed map[string]bool
	if hasWhitelist {
		allowed = make(map[string]bool, len(whitelist))
		for _, name := range whitelist {
			if name == delegateToolName {
				continue
			}
			if !t.cfg.Registry.Has(name) {
				return nil, nil, fmt.Errorf("unknown tool: %q", name)
			}
			allowed[name] = true
		}
	}

	toolSet := engine.ToolSet{}
	var defs []toolspec.Definition

	for _, def := range allDefs {
		if def.Name == delegateToolName {
			continue
		}
		if allowed != nil && !allowed[def.Name] {
			continue
		}
		defs = append(defs, def)
		toolSet[def.Name] = func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			result, err := t.cfg.Registry.Execute(ctx, call.Name, call.Arguments)
			return ai.TextContent{Text: result}, err
		}
	}

	return toolSet, defs, nil
}

// extractLastAssistantText returns the text content of the last assistant message.
func extractLastAssistantText(history []ai.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		msg, ok := history[i].(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range msg.Content {
			if tc, ok := block.(ai.TextContent); ok {
				return tc.Text
			}
		}
	}
	return ""
}

// truncateResult truncates a string to maxChars runes, appending a marker if truncated.
func truncateResult(s string, maxChars int) string {
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxChars]) + "\n[truncated]"
}

func parseDelegateTasks(args map[string]any) ([]delegateTaskConfig, error) {
	tasksRaw, ok := args["tasks"]
	if !ok {
		return nil, fmt.Errorf("tasks is required")
	}
	tasksSlice, ok := tasksRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("tasks must be an array")
	}

	tasks := make([]delegateTaskConfig, 0, len(tasksSlice))
	seen := make(map[string]bool, len(tasksSlice))

	for i, raw := range tasksSlice {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tasks[%d]: must be an object", i)
		}

		tc := delegateTaskConfig{}

		id, ok := obj["id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("tasks[%d]: id is required", i)
		}
		if seen[id] {
			return nil, fmt.Errorf("tasks[%d]: duplicate id %q", i, id)
		}
		seen[id] = true
		tc.ID = id

		task, ok := obj["task"].(string)
		if !ok || task == "" {
			return nil, fmt.Errorf("tasks[%d]: task is required", i)
		}
		tc.Task = task

		if model, ok := obj["model"].(string); ok {
			tc.Model = model
		}
		if system, ok := obj["system"].(string); ok {
			tc.System = system
		}
		if maxTurns, ok := obj["max_turns"].(float64); ok {
			tc.MaxTurns = int(maxTurns)
		}
		if timeoutSeconds, ok := obj["timeout_seconds"].(float64); ok {
			tc.TimeoutSeconds = int(timeoutSeconds)
		}
		if toolsRaw, ok := obj["tools"].([]any); ok {
			tc.HasTools = true
			for _, tr := range toolsRaw {
				if s, ok := tr.(string); ok {
					tc.Tools = append(tc.Tools, s)
				}
			}
		}

		tasks = append(tasks, tc)
	}

	return tasks, nil
}
