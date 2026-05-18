package delegate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	delegateToolName = "delegate"

	// defaultTimeout is the wall-clock deadline applied to every subagent run.
	// Overridable globally via DelegateConfig.DefaultTimeout (runner.subagent_timeout
	// in admin settings) or per-preset via the timeout front-matter field.
	defaultTimeout        = 15 * time.Minute
	defaultMaxConcurrency = 10
)

// DelegateConfig holds the dependencies needed to spawn subagent loops.
type DelegateConfig struct {
	Stream        providers.StreamFunc
	Registry      *tools.Registry
	Model         ai.Model
	System        string
	Emit          func(agent.LoopEvent) // optional event emitter for observability
	Presets       *PresetRegistry       // loaded agent presets (nil = no presets)
	Hooks         *hooks.HookSet        // inherited by subagents (nil = no hooks)
	ToolLifecycle *agent.ToolLifecycle

	// Configurable limits (zero = use defaults).
	MaxConcurrency int           // max parallel subagent goroutines
	DefaultTimeout time.Duration // default subagent wall-clock timeout (0 = 15m)
}

func (c DelegateConfig) maxConcurrency() int {
	if c.MaxConcurrency > 0 {
		return c.MaxConcurrency
	}
	return defaultMaxConcurrency
}

func (c DelegateConfig) subagentTimeout() time.Duration {
	if c.DefaultTimeout > 0 {
		return c.DefaultTimeout
	}
	return defaultTimeout
}

// DelegateTool spawns child agent loops for bounded subtasks.
type DelegateTool struct {
	cfg DelegateConfig
	sem chan struct{} // concurrency semaphore
}

// NewDelegateTool creates an agent tool with the given configuration.
func NewDelegateTool(cfg DelegateConfig) *DelegateTool {
	return &DelegateTool{
		cfg: cfg,
		sem: make(chan struct{}, cfg.maxConcurrency()),
	}
}

// DelegateDefinition returns the tool definition without requiring a live config.
// presets is optional — when provided, the preset enum is included in the schema.
func DelegateDefinition(presets *PresetRegistry) tools.Definition {
	var presetNames []string
	var presetDesc string
	if presets != nil && len(presets.Names()) > 0 {
		presetNames = presets.Names()
		presetDesc = "Preset agent configuration. Available: " + strings.Join(presetNames, ", ") +
			". Preset values are defaults that explicit fields override."
	}

	return tools.Definition{
		Name:        delegateToolName,
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
							"id":     map[string]any{"type": "string", "description": "Optional task identifier for result mapping. Auto-generated as task_0, task_1… if omitted."},
							"task":   map[string]any{"type": "string", "description": "Task description for the subagent. Include any context the subagent needs inline."},
							"preset": presetField(presetNames, presetDesc),
							"model": map[string]any{
								"type":        "string",
								"description": "Optional model override (e.g. 'claude-haiku-4-5-20251001'). Defaults to parent model.",
							},
						},
						"required": []string{"task"},
					},
				},
			},
			"required": []string{"tasks"},
		},
	}
}

func (t *DelegateTool) Definition() tools.Definition {
	return DelegateDefinition(t.cfg.Presets)
}

func (t *DelegateTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	tasks, err := parseDelegateTasks(args)
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}
	if len(tasks) == 0 {
		return "", fmt.Errorf("agent: at least one task is required")
	}

	results := make(map[string]taskResult, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, task := range tasks {
		wg.Add(1)
		go func(tc delegateTaskConfig) {
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

type delegateTaskConfig struct {
	ID     string
	Task   string
	Preset string
	Model  string
	// Fields below are populated by presets only.
	System         string
	Tools          []string
	HasTools       bool
	TimeoutSeconds int
}

// applyPreset merges preset defaults into the task config.
// Explicit task fields take precedence over preset values.
func (tc *delegateTaskConfig) applyPreset(p DelegatePreset) {
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
	if tc.TimeoutSeconds == 0 && p.Timeout > 0 {
		tc.TimeoutSeconds = int(p.Timeout.Seconds())
	}
}

type taskResult struct {
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Complete bool   `json:"complete"` // true if agent stopped naturally (not max_turns/timeout)
}

func (t *DelegateTool) emit(ev agent.LoopEvent) {
	if t.cfg.Emit != nil {
		t.cfg.Emit(ev)
	}
}

func (t *DelegateTool) runSubAgent(parentCtx context.Context, tc delegateTaskConfig) (result taskResult) {
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
		presets := t.cfg.Presets
		if presets == nil {
			return taskResult{Error: "no presets available"}
		}
		if p, ok := presets.Lookup(tc.Preset); ok {
			tc.applyPreset(p)
		} else {
			return taskResult{Error: fmt.Sprintf("unknown preset: %q (available: %s)",
				tc.Preset, strings.Join(presets.Names(), ", "))}
		}
	}

	// The wall-clock timeout is the only limit applied to subagent runs.
	// It kills the run if it takes too long regardless of what the agent is doing.
	// Configurable via runner.subagent_timeout (admin settings) or per-preset
	// timeout front-matter. Default: 15m.
	timeout := t.cfg.subagentTimeout()
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

	// Inherit parent session context so sub-agent spans link to the parent trace.
	subMeta := hooks.HookMeta{
		SessionID: memory.SessionIDFromContext(ctx) + ":subagent:" + tc.ID,
		UserID:    memory.UserIDFromContext(ctx),
		AgentID:   memory.AgentIDFromContext(ctx),
	}

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Stream:          t.cfg.Stream,
		Model:           model,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
	},
		agent.WithSystem(system),
		agent.WithHooks(t.cfg.Hooks, subMeta),
		agent.WithToolLifecycle(t.cfg.ToolLifecycle),
	)
	if err != nil {
		return taskResult{Error: fmt.Sprintf("create runner: %v", err)}
	}

	messages := []ai.Message{
		ai.UserMessage{Content: tc.Task},
	}

	log.Info("subagent started", "preset", tc.Preset, "model", model.Name, "timeout", timeout)
	start := time.Now()

	t.emit(SubDelegateStarted{TaskID: tc.ID, Preset: tc.Preset})

	history, err := runner.Run(ctx, messages, nil)
	duration := time.Since(start)

	if err != nil {
		log.Error("subagent failed", "duration", duration, "error", err)
		t.emit(SubDelegateFinished{TaskID: tc.ID, Duration: duration, Error: err.Error()})
		return taskResult{Error: err.Error()}
	}

	log.Info("subagent finished", "duration", duration)

	output, stopReason := extractLastAssistant(history)
	complete := stopReason == ai.StopReasonStop || stopReason == ai.StopReasonLength

	t.emit(SubDelegateFinished{TaskID: tc.ID, Duration: duration})

	return taskResult{Output: output, Complete: complete}
}

// buildScopedTools creates a filtered tool set for the child agent.
// It always excludes "agent" to prevent recursion.
// If hasWhitelist is true, only the listed tools are included (empty list = no tools).
// If hasWhitelist is false, all parent tools (minus agent) are available.
func (t *DelegateTool) buildScopedTools(whitelist []string, hasWhitelist bool) (agent.ToolSet, []tools.Definition, error) {
	if hasWhitelist {
		// Filter out "agent" from whitelist.
		filtered := make([]string, 0, len(whitelist))
		for _, name := range whitelist {
			if name != delegateToolName {
				filtered = append(filtered, name)
			}
		}
		return agent.ToolSetFromRegistryFiltered(t.cfg.Registry, filtered)
	}

	// No whitelist: all tools minus "agent".
	allDefs := t.cfg.Registry.Definitions()
	names := make([]string, 0, len(allDefs))
	for _, def := range allDefs {
		if def.Name != delegateToolName {
			names = append(names, def.Name)
		}
	}
	return agent.ToolSetFromRegistryFiltered(t.cfg.Registry, names)
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

		id, _ := obj["id"].(string)
		if id == "" {
			id = fmt.Sprintf("task_%d", i)
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
		if model, ok := obj["model"].(string); ok {
			tc.Model = model
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
