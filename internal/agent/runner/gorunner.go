package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/plugins/providers/anthropic"
	"github.com/vaayne/anna/plugins/providers/openai"
	openairesponse "github.com/vaayne/anna/plugins/providers/openai-response"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/tools"
)

const maxToolIterations = 40

// GoRunnerConfig configures the Go runner.
type GoRunnerConfig struct {
	API         string // provider key: "anthropic", "openai"
	Model       string // e.g. "claude-sonnet-4-20250514"
	APIKey      string
	BaseURL     string             // optional provider base URL override
	WorkDir     string             // working directory for tool execution
	Workspace   string             // workspace dir for skills/memory (e.g. ~/.anna/workspace)
	AnnaHome    string             // anna home directory (e.g. ~/.anna)
	System      string             // optional system prompt override (bypasses default prompt building)
	ExtraTools  []tools.Tool       // additional tools to register
	UserDataDir string             // per-user data directory for sandbox enforcement (empty = no sandbox)
	HookPlugins []hooks.HookPlugin // hook plugins for the engine loop
}

// GoRunner implements Runner by calling LLM providers directly via Engine.
type GoRunner struct {
	eng    *engine.Engine
	reg    *ai.Registry
	tools  *tools.Registry
	hooks  *hooks.HookSet
	model  ai.Model
	apiKey string
	system string

	mu           sync.Mutex
	lastActivity time.Time
	log          *slog.Logger
}

// NewGoRunner creates a Go runner with built-in providers.
func NewGoRunner(_ context.Context, cfg GoRunnerConfig) (*GoRunner, error) {
	if cfg.API == "" {
		return nil, fmt.Errorf("go runner: api is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("go runner: model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("go runner: api_key is required")
	}

	reg := ai.NewRegistry()
	reg.Register(anthropic.New(anthropic.Config{BaseURL: cfg.BaseURL}))
	reg.Register(openai.New(openai.Config{BaseURL: cfg.BaseURL}))
	reg.Register(openairesponse.New(openairesponse.Config{BaseURL: cfg.BaseURL}))

	system := cfg.System
	if system == "" {
		system = BuildSystemPromptFromDB(DBPromptParams{
			AnnaHome:  cfg.AnnaHome,
			Workspace: cfg.Workspace,
			Cwd:       cfg.WorkDir,
		})
	}

	eng := &engine.Engine{Providers: reg}
	model := ai.Model{API: cfg.API, Name: cfg.Model}

	toolReg := tools.NewRegistry(cfg.WorkDir, cfg.UserDataDir)
	for _, t := range cfg.ExtraTools {
		toolReg.Register(t)
	}

	// Load agent presets from filesystem.
	presets := tools.NewPresetRegistry(tools.LoadAgentPresets(tools.LoadAgentPresetsConfig{
		AnnaHome:  cfg.AnnaHome,
		Workspace: cfg.Workspace,
		Cwd:       cfg.WorkDir,
	}))

	var hookSet *hooks.HookSet
	if len(cfg.HookPlugins) > 0 {
		hookSet = hooks.NewHookSet(cfg.HookPlugins)
	}

	toolReg.Register(tools.NewAgentTool(tools.AgentConfig{
		Engine:   eng,
		Registry: toolReg,
		Model:    model,
		APIKey:   cfg.APIKey,
		System:   system,
		Presets:  presets,
		Hooks:    hookSet,
	}))

	return &GoRunner{
		eng:          eng,
		reg:          reg,
		tools:        toolReg,
		hooks:        hookSet,
		model:        model,
		apiKey:       cfg.APIKey,
		system:       system,
		lastActivity: time.Now(),
		log:          slog.With("component", "go_runner"),
	}, nil
}

// Chat runs the Engine agent loop with the provided history and forwards events.
func (r *GoRunner) Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event {
	out := make(chan Event, 100)

	r.mu.Lock()
	r.lastActivity = time.Now()
	r.mu.Unlock()

	go func() {
		defer close(out)

		messages := make([]ai.Message, len(history))
		copy(messages, history)
		messages = append(messages, ai.UserMessage{Content: message})

		cfg := engine.LoopConfig{
			Model:           r.model,
			StreamOptions:   ai.StreamOptions{APIKey: r.apiKey},
			MaxTurns:        maxToolIterations,
			Tools:           r.buildToolSet(),
			ToolDefinitions: r.tools.Definitions(),
			System:          r.system,
			Hooks:           r.hooks,
		}

		if _, err := r.eng.Run(ctx, cfg, messages, func(e engine.LoopEvent) {
			for _, evt := range convertLoopEvent(e) {
				out <- evt
			}
		}); err != nil {
			out <- Event{Err: err}
		}
	}()

	return out
}

// Alive always returns true — the Go runner has no subprocess to die.
func (r *GoRunner) Alive() bool { return true }

// LastActivity returns the time of the last Chat call.
func (r *GoRunner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}

// Close shuts down any subprocess-backed tools owned by the runner.
func (r *GoRunner) Close() error {
	if r.tools != nil {
		_ = r.tools.Close()
	}
	return nil
}

// buildToolSet adapts tool.Registry to engine.ToolSet for Engine.
func (r *GoRunner) buildToolSet() engine.ToolSet {
	set := engine.ToolSet{}
	for _, def := range r.tools.Definitions() {
		name := def.Name
		set[name] = func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error) {
			result, err := r.tools.Execute(ctx, name, call.Arguments)
			return ai.TextContent{Text: result}, err
		}
	}
	return set
}

// convertLoopEvent bridges engine.LoopEvent to Event(s).
func convertLoopEvent(e engine.LoopEvent) []Event {
	switch e := e.(type) {
	case engine.AssistantDelta:
		if d, ok := e.Event.(ai.EventTextDelta); ok && d.Text != "" {
			return []Event{{Text: d.Text}}
		}

	case engine.AssistantFinished:
		// Emit Store events for tool calls in the final message.
		var events []Event
		for _, block := range e.Message.Content {
			if _, ok := block.(ai.ToolCall); ok {
				// Store the full assistant message (text + all tool calls) once
				// when we see the first tool call.
				msg := ai.AssistantMessage{Content: e.Message.Content}
				events = append(events, Event{Store: msg})
				return events
			}
		}
		return events

	case engine.ToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			Tool:   e.ToolCall.Name,
			Status: "running",
			Input:  summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
		}}}

	case engine.ToolFinished:
		status := "done"
		detail := summarizeToolResult(e.Result)
		if e.Result.IsError {
			status = "error"
		}
		return []Event{
			{ToolUse: &ToolUseEvent{
				Tool:   e.Result.ToolName,
				Status: status,
				Detail: detail,
			}},
			{Store: e.Result},
		}

	case engine.AgentErrored:
		return []Event{{Err: e.Err}}
	}

	return nil
}

// summarizeToolResult returns a short human-readable summary of a tool result.
func summarizeToolResult(result ai.ToolResultMessage) string {
	var text string
	for _, block := range result.Content {
		if tc, ok := block.(ai.TextContent); ok {
			text = tc.Text
			break
		}
	}
	if text == "" {
		return ""
	}

	if result.IsError {
		// For errors, show the first line.
		if idx := strings.Index(text, "\n"); idx > 0 {
			text = text[:idx]
		}
		if len(text) > 120 {
			return text[:117] + "..."
		}
		return text
	}

	// For success, produce a brief summary based on tool type.
	lines := strings.Count(text, "\n") + 1
	runeCount := len([]rune(text))

	switch {
	case runeCount <= 80:
		// Short result — show inline.
		if idx := strings.Index(text, "\n"); idx > 0 {
			text = text[:idx]
		}
		return text
	case lines > 1:
		return fmt.Sprintf("%d lines", lines)
	default:
		return fmt.Sprintf("%d chars", runeCount)
	}
}

// summarizeToolInput returns a short human-readable summary of tool arguments.
func summarizeToolInput(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 80 {
				return cmd[:80] + "..."
			}
			return cmd
		}
	case "read":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	case "write":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	case "edit":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	}
	return ""
}
