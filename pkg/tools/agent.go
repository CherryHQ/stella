package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
)

const (
	agentToolName = "agent"

	// Defaults (overridable via AgentConfig).
	defaultMaxTurns       = 10
	defaultTimeoutSeconds = 120
	defaultMaxTasks       = 5
	defaultMaxConcurrency = 3
	defaultMaxResultChars = 16000 // ~4096 tokens
)

// AgentConfig holds the dependencies needed to spawn subagent loops.
type AgentConfig struct {
	Engine   *engine.Engine
	Registry *Registry
	Model    ai.Model
	APIKey   string
	BaseURL  string
	System   string
	Emit     func(engine.LoopEvent) // optional event emitter for observability
	Presets  *PresetRegistry        // loaded agent presets (nil = no presets)
	Hooks    *hooks.HookSet         // inherited by subagents (nil = no hooks)

	// Configurable limits (zero = use defaults).
	MaxTasks       int // max tasks per invocation
	MaxConcurrency int // max parallel subagent goroutines
	MaxResultChars int // max runes in result output
}

func (c AgentConfig) maxTasks() int {
	if c.MaxTasks > 0 {
		return c.MaxTasks
	}
	return defaultMaxTasks
}

func (c AgentConfig) maxConcurrency() int {
	if c.MaxConcurrency > 0 {
		return c.MaxConcurrency
	}
	return defaultMaxConcurrency
}

func (c AgentConfig) maxResultChars() int {
	if c.MaxResultChars > 0 {
		return c.MaxResultChars
	}
	return defaultMaxResultChars
}

// AgentTool spawns child agent loops for bounded subtasks.
type AgentTool struct {
	cfg AgentConfig
	sem chan struct{} // concurrency semaphore
}

// NewAgentTool creates an agent tool with the given configuration.
func NewAgentTool(cfg AgentConfig) *AgentTool {
	return &AgentTool{
		cfg: cfg,
		sem: make(chan struct{}, cfg.maxConcurrency()),
	}
}

// AgentDefinition returns the tool definition without requiring a live config.
// presets is optional — when provided, the preset enum is included in the schema.
func AgentDefinition(presets *PresetRegistry) Definition {
	var presetNames []string
	var presetDesc string
	if presets != nil && len(presets.Names()) > 0 {
		presetNames = presets.Names()
		presetDesc = "Preset agent configuration. Available: " + strings.Join(presetNames, ", ") +
			". Preset values are defaults that explicit fields override."
	}

	return Definition{
		Name:        agentToolName,
		Description: "Spawn one or more subagents with isolated context. Multiple tasks run in parallel. Use for focused subtasks like research, code review, or drafting that benefit from fresh context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "One or more subtasks to spawn as subagents. Multiple tasks run in parallel.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "string", "description": "Task identifier for result mapping."},
							"task":   map[string]any{"type": "string", "description": "Task description for the subagent."},
							"preset": presetField(presetNames, presetDesc),
							"context": map[string]any{
								"type":        "string",
								"description": "Optional context from parent (file contents, decisions, constraints) prepended to the task message.",
							},
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
								"description": "Optional tool whitelist. Only these tools will be available. Defaults to all parent tools minus agent.",
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

func (t *AgentTool) Definition() Definition {
	return AgentDefinition(t.cfg.Presets)
}

func (t *AgentTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	tasks, err := parseAgentTasks(args)
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}
	if len(tasks) == 0 {
		return "", fmt.Errorf("agent: at least one task is required")
	}
	if len(tasks) > t.cfg.maxTasks() {
		return "", fmt.Errorf("agent: too many tasks (%d), maximum is %d", len(tasks), t.cfg.maxTasks())
	}

	results := make(map[string]taskResult, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, task := range tasks {
		wg.Add(1)
		go func(tc agentTaskConfig) {
			defer wg.Done()

			// Acquire semaphore slot.
			select {
			case t.sem <- struct{}{}:
				defer func() { <-t.sem }()
			case <-ctx.Done():
				mu.Lock()
				results[tc.ID] = taskResult{Error: ctx.Err().Error()}
				mu.Unlock()
				return
			}

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
		return "", fmt.Errorf("agent: marshal results: %w", err)
	}
	return string(out), nil
}

type agentTaskConfig struct {
	ID             string
	Task           string
	Preset         string
	Context        string // parent-provided context prepended to task message
	Model          string
	System         string
	Tools          []string // nil = all tools; empty = no tools
	HasTools       bool     // true when "tools" key was present in args
	MaxTurns       int
	TimeoutSeconds int
}

// applyPreset merges preset defaults into the task config.
// Explicit task fields take precedence over preset values.
func (tc *agentTaskConfig) applyPreset(p AgentPreset) {
	if tc.Model == "" && p.Model != "" {
		tc.Model = p.Model
	}
	if tc.System == "" && p.System != "" {
		tc.System = p.System
	}
	if !tc.HasTools && p.HasTools {
		tc.Tools = p.Tools
		tc.HasTools = true
	}
	if tc.MaxTurns == 0 && p.MaxTurns > 0 {
		tc.MaxTurns = p.MaxTurns
	}
	if tc.TimeoutSeconds == 0 && p.Timeout > 0 {
		tc.TimeoutSeconds = int(p.Timeout.Seconds())
	}
}

type taskResult struct {
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Complete bool   `json:"complete"` // true if agent stopped naturally (not max_turns/timeout)
}

func (t *AgentTool) emit(ev engine.LoopEvent) {
	if t.cfg.Emit != nil {
		t.cfg.Emit(ev)
	}
}

func (t *AgentTool) runSubAgent(parentCtx context.Context, tc agentTaskConfig) (result taskResult) {
	log := slog.With("component", "agent", "task_id", tc.ID)

	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			result = taskResult{Error: fmt.Sprintf("panic: %v\n%s", r, stack)}
			log.Error("subagent panicked", "error", r, "stack", stack)
		}
	}()

	// Apply preset defaults if specified.
	if tc.Preset != "" {
		if t.cfg.Presets == nil {
			return taskResult{Error: "no presets available"}
		}
		if p, ok := t.cfg.Presets.Lookup(tc.Preset); ok {
			tc.applyPreset(p)
		} else {
			return taskResult{Error: fmt.Sprintf("unknown preset: %q (available: %s)",
				tc.Preset, strings.Join(t.cfg.Presets.Names(), ", "))}
		}
	}

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
		StreamOptions:   ai.StreamOptions{APIKey: t.cfg.APIKey, BaseURL: t.cfg.BaseURL},
		MaxTurns:        maxTurns,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
		System:          system,
		Hooks:           t.cfg.Hooks,
	}

	// Build user message: optional context + task.
	userContent := tc.Task
	if tc.Context != "" {
		userContent = tc.Context + "\n\n---\n\n" + tc.Task
	}

	messages := []ai.Message{
		ai.UserMessage{Content: userContent},
	}

	log.Info("subagent started", "preset", tc.Preset, "model", model.Name, "max_turns", maxTurns)
	start := time.Now()

	t.emit(SubAgentStarted{TaskID: tc.ID, Preset: tc.Preset})

	history, err := t.cfg.Engine.Run(ctx, cfg, messages, nil)
	duration := time.Since(start)

	if err != nil {
		log.Error("subagent failed", "duration", duration, "error", err)
		t.emit(SubAgentFinished{TaskID: tc.ID, Duration: duration, Error: err.Error()})
		return taskResult{Error: err.Error()}
	}

	log.Info("subagent finished", "duration", duration)

	output, stopReason := extractLastAssistant(history)
	output = truncateResult(output, t.cfg.maxResultChars())

	complete := stopReason == ai.StopReasonStop || stopReason == ai.StopReasonLength

	t.emit(SubAgentFinished{TaskID: tc.ID, Duration: duration})

	return taskResult{Output: output, Complete: complete}
}

// buildScopedTools creates a filtered tool set for the child agent.
// It always excludes "agent" to prevent recursion.
// If hasWhitelist is true, only the listed tools are included (empty list = no tools).
// If hasWhitelist is false, all parent tools (minus agent) are available.
func (t *AgentTool) buildScopedTools(whitelist []string, hasWhitelist bool) (engine.ToolSet, []Definition, error) {
	allDefs := t.cfg.Registry.Definitions()

	// Build allowed set from whitelist, if provided.
	var allowed map[string]bool
	if hasWhitelist {
		allowed = make(map[string]bool, len(whitelist))
		for _, name := range whitelist {
			if name == agentToolName {
				continue
			}
			if !t.cfg.Registry.Has(name) {
				return nil, nil, fmt.Errorf("unknown tool: %q", name)
			}
			allowed[name] = true
		}
	}

	toolSet := engine.ToolSet{}
	var defs []Definition

	for _, def := range allDefs {
		if def.Name == agentToolName {
			continue
		}
		if allowed != nil && !allowed[def.Name] {
			continue
		}
		defs = append(defs, def)
		name := def.Name // capture for closure safety
		toolSet[name] = func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			result, err := t.cfg.Registry.Execute(ctx, call.Name, call.Arguments)
			return ai.TextContent{Text: result}, err
		}
	}

	return toolSet, defs, nil
}

// extractLastAssistant returns the text content and stop reason of the last assistant message.
func extractLastAssistant(history []ai.Message) (string, ai.StopReason) {
	for i := len(history) - 1; i >= 0; i-- {
		msg, ok := history[i].(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range msg.Content {
			if tc, ok := block.(ai.TextContent); ok {
				return tc.Text, msg.StopReason
			}
		}
		return "", msg.StopReason
	}
	return "", ""
}

// truncateResult truncates a string to maxChars runes, appending a marker if truncated.
func truncateResult(s string, maxChars int) string {
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxChars]) + "\n[truncated]"
}

func parseAgentTasks(args map[string]any) ([]agentTaskConfig, error) {
	tasksRaw, ok := args["tasks"]
	if !ok {
		return nil, fmt.Errorf("tasks is required")
	}
	tasksSlice, ok := tasksRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("tasks must be an array")
	}

	tasks := make([]agentTaskConfig, 0, len(tasksSlice))
	seen := make(map[string]bool, len(tasksSlice))

	for i, raw := range tasksSlice {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tasks[%d]: must be an object", i)
		}

		tc := agentTaskConfig{}

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

		if preset, ok := obj["preset"].(string); ok {
			tc.Preset = preset
		}
		if ctx, ok := obj["context"].(string); ok {
			tc.Context = ctx
		}
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

// presetField builds the schema entry for the preset field.
// When no presets are available, it returns a plain string field.
func presetField(names []string, desc string) map[string]any {
	if len(names) == 0 {
		return map[string]any{
			"type":        "string",
			"description": "Preset agent configuration name (no presets currently loaded).",
		}
	}
	return map[string]any{
		"type":        "string",
		"enum":        names,
		"description": desc,
	}
}
