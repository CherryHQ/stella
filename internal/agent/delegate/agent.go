package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/core/agentctx"
	"github.com/CherryHQ/stella/internal/core/toolmeta"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	delegateToolName = "delegate"

	// defaultTimeout is the wall-clock deadline applied to every delegate run.
	// Overridable globally via DelegateConfig.DefaultTimeout (runner.delegate_timeout
	// in admin settings) or per-preset via the timeout front-matter field.
	defaultTimeout        = 15 * time.Minute
	defaultMaxConcurrency = 10
)

// DelegateConfig holds the dependencies needed to spawn delegate loops.
type DelegateConfig struct {
	Stream   providers.StreamFunc
	Registry *tools.Registry
	Model    ai.Model
	System   string
	Emit     func(agent.LoopEvent) // optional event emitter for observability
	Presets  *PresetRegistry       // loaded delegate presets (nil = no presets)
	// ToolMeta declares the generated tools, so a preset's tools: list can name
	// a family. Nil means exact-name matching only.
	ToolMeta      *toolmeta.Registry
	Hooks         *hooks.HookSet // inherited by delegates (nil = no hooks)
	ToolLifecycle *agent.ToolLifecycle
	SessionRunner SessionRunner // runs delegate work through persistent agent sessions

	// Configurable limits (zero = use defaults).
	MaxConcurrency int           // max parallel delegate goroutines
	DefaultTimeout time.Duration // default delegate wall-clock timeout (0 = 15m)
}

// SessionRunner runs delegate work through the owning agent pool so child
// transcripts are persisted and resumable like normal sessions.
type SessionRunner interface {
	RunDelegateSession(context.Context, SessionRunRequest) (SessionRunResult, error)
}

// SessionRunRequest describes a single persisted delegate turn.
type SessionRunRequest struct {
	SessionID     string
	Task          string
	Model         string
	System        string
	ExcludedTools []string
	Timeout       time.Duration
	ProjectID     string
}

// SessionRunResult is the output from a persisted delegate session.
type SessionRunResult struct {
	SessionID       string
	Output          string
	OutputTruncated bool
	Complete        bool
}

// ManagedSessionRequest is one synchronous managed Session turn. It is shared
// with the Session tool so that it resolves presets, timeout, system override,
// and tool exclusions through the same path as delegate.
type ManagedSessionRequest struct {
	SessionID string
	Message   string
	Preset    string
}

// ManagedSessionResult is the bounded caller-facing result of a managed
// Session turn. Output is bounded by the Session tool, not this delegate layer.
type ManagedSessionResult struct {
	SessionID       string
	Output          string
	OutputTruncated bool
	Complete        bool
}

func (c DelegateConfig) maxConcurrency() int {
	if c.MaxConcurrency > 0 {
		return c.MaxConcurrency
	}
	return defaultMaxConcurrency
}

func (c DelegateConfig) delegateTimeout() time.Duration {
	if c.DefaultTimeout > 0 {
		return c.DefaultTimeout
	}
	return defaultTimeout
}

// DelegateTool spawns child loops for bounded subtasks.
type DelegateTool struct {
	cfg DelegateConfig
	sem chan struct{} // concurrency semaphore
}

// NewDelegateTool creates a delegate tool with the given configuration.
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
		presetDesc = "Preset delegate configuration. Available: " + strings.Join(presetNames, ", ") +
			". Preset values are defaults that explicit fields override."
	}

	return tools.Definition{
		Name:        delegateToolName,
		Description: "Delegate one or more focused subtasks to isolated child loops. Multiple tasks run in parallel. Use for research, code review, or drafting that benefit from fresh context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "One or more subtasks to delegate. Multiple tasks run in parallel.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string", "description": "Optional task identifier for result mapping. Auto-generated as task_0, task_1… if omitted."},
							"task":       map[string]any{"type": "string", "description": "Task description for the delegate. Include any context it needs inline."},
							"session_id": map[string]any{"type": "string", "description": "Optional delegate session ID to resume. If omitted, Stella creates a new persistent delegate session and returns its session_id."},
							"preset":     presetField(presetNames, presetDesc),
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

			result := t.runDelegate(ctx, tc)
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

// RunManagedSession executes one Session-tool create or send through the
// existing delegate path. Keeping this as a single-task adapter avoids a second
// preset or timeout implementation that could drift from delegate.
func (t *DelegateTool) RunManagedSession(ctx context.Context, req ManagedSessionRequest) (ManagedSessionResult, error) {
	result := t.runDelegate(ctx, delegateTaskConfig{
		ID:        "session",
		Task:      req.Message,
		Preset:    req.Preset,
		SessionID: req.SessionID,
	})
	if result.Error != "" {
		err := result.cause
		if err == nil {
			err = errors.New(result.Error)
		}
		return ManagedSessionResult{SessionID: result.SessionID, Output: result.Output, OutputTruncated: result.OutputTruncated}, err
	}
	return ManagedSessionResult{SessionID: result.SessionID, Output: result.Output, OutputTruncated: result.OutputTruncated, Complete: result.Complete}, nil
}

type delegateTaskConfig struct {
	ID        string
	Task      string
	Preset    string
	Model     string
	SessionID string
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
	Output          string `json:"output"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Error           string `json:"error,omitempty"`
	Complete        bool   `json:"complete"` // true if agent stopped without error
	cause           error
}

func (t *DelegateTool) emit(ev agent.LoopEvent) {
	if t.cfg.Emit != nil {
		t.cfg.Emit(ev)
	}
}

func (t *DelegateTool) runDelegate(parentCtx context.Context, tc delegateTaskConfig) (result taskResult) {
	log := slog.With("component", "delegate", "task_id", tc.ID)

	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			result = taskResult{Error: fmt.Sprintf("panic: %v\n%s", r, stack)}
			log.Error("delegate panicked", "error", r, "stack", stack)
		}
	}()

	parentCtx, err := agentctx.EnterSessionCall(parentCtx, memory.SessionIDFromContext(parentCtx), tc.SessionID)
	if err != nil {
		return taskResult{SessionID: tc.SessionID, Error: err.Error(), cause: err}
	}

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

	// The wall-clock timeout is the only limit applied to delegate runs.
	// Configurable via runner.delegate_timeout (admin settings) or per-preset
	// timeout front-matter. Default: 15m.
	timeout := t.cfg.delegateTimeout()
	if tc.TimeoutSeconds > 0 {
		timeout = time.Duration(tc.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()
	// The parent may be a channel chat turn. A delegate is not that chat, so it
	// must not inherit the binding that proves one — the tool exclusion above is
	// the second lock on the same door.
	ctx = agentctx.WithoutChatBinding(ctx)

	excludedTools := t.excludedTools(tc.Tools, tc.HasTools)

	// Build system prompt: base + optional additions.
	system := t.cfg.System
	if tc.System != "" {
		system += "\n\n" + tc.System
	}

	if t.cfg.SessionRunner == nil {
		return taskResult{Error: "persistent delegate session runner is not configured"}
	}

	model := t.cfg.Model.Name
	if t.cfg.Model.Provider != "" {
		model = t.cfg.Model.Provider + "/" + model
	}
	if tc.Model != "" {
		model = tc.Model
	}

	log.Info("delegate started", "preset", tc.Preset, "model", model, "timeout", timeout, "session_id", tc.SessionID)
	start := time.Now()

	t.emit(DelegateStarted{TaskID: tc.ID, Preset: tc.Preset})

	sessionResult, err := t.cfg.SessionRunner.RunDelegateSession(ctx, SessionRunRequest{
		SessionID:     tc.SessionID,
		Task:          tc.Task,
		Model:         model,
		System:        system,
		ExcludedTools: excludedTools,
		Timeout:       timeout,
	})
	duration := time.Since(start)

	if err != nil {
		log.Error("delegate failed", "duration", duration, "error", err, "session_id", sessionResult.SessionID)
		t.emit(DelegateFinished{TaskID: tc.ID, Duration: duration, Error: err.Error()})
		return taskResult{Output: sessionResult.Output, OutputTruncated: sessionResult.OutputTruncated, SessionID: sessionResult.SessionID, Error: err.Error(), cause: err}
	}

	log.Info("delegate finished", "duration", duration, "session_id", sessionResult.SessionID)

	t.emit(DelegateFinished{TaskID: tc.ID, Duration: duration})

	return taskResult{Output: sessionResult.Output, OutputTruncated: sessionResult.OutputTruncated, SessionID: sessionResult.SessionID, Complete: sessionResult.Complete}
}

// excludedTools returns tools hidden for this delegate run. Nested collaboration
// is bounded by context-carried depth and ancestry, not permanent tool hiding.
func (t *DelegateTool) excludedTools(whitelist []string, hasWhitelist bool) []string {
	blocked := make(map[string]struct{})
	if hasWhitelist {
		// The whitelist is a user-written file. A preset that lists "scheduler"
		// means the family, so it keeps granting the same capability after the
		// family was split into one tool per action.
		for _, def := range t.cfg.Registry.Definitions() {
			if !t.cfg.ToolMeta.MatchAnyName(whitelist, def.Name) {
				blocked[def.Name] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(blocked))
	for name := range blocked {
		out = append(out, name)
	}
	return out
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
	seenSessionIDs := make(map[string]bool, len(tasksSlice))

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
		if sessionID, ok := obj["session_id"].(string); ok {
			tc.SessionID = sessionID
		}
		if tc.SessionID != "" {
			if seenSessionIDs[tc.SessionID] {
				return nil, fmt.Errorf("tasks[%d]: duplicate session_id %q; concurrent writes to the same session are not allowed", i, tc.SessionID)
			}
			seenSessionIDs[tc.SessionID] = true
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
			"description": "Preset delegate configuration name (no presets currently loaded).",
		}
	}
	return map[string]any{
		"type":        "string",
		"enum":        names,
		"description": desc,
	}
}
