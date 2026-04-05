package runner

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/runner/builtin"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
	agenttool "github.com/vaayne/anna/plugins/tools/agent"

	pluginproviders "github.com/vaayne/anna/plugins/providers"
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

// GoRunner implements Runner by calling LLM providers directly via agent.Runner.
type GoRunner struct {
	runner *agent.Runner
	reg    *providers.Registry
	tools  *tools.Registry

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

	reg, err := buildProviderRegistry(cfg)
	if err != nil {
		return nil, err
	}

	system := cfg.System
	if system == "" {
		system = BuildSystemPromptFromDB(DBPromptParams{
			AnnaHome:  cfg.AnnaHome,
			Workspace: cfg.Workspace,
			Cwd:       cfg.WorkDir,
		})
	}

	model := ai.Model{API: cfg.API, Name: cfg.Model}
	toolReg := buildToolRegistry(cfg)
	presets := buildAgentPresets(cfg)
	hookSet := buildHookSet(cfg)

	toolReg.Register(agenttool.NewAgentTool(agenttool.AgentConfig{
		Providers: reg,
		Registry:  toolReg,
		Model:     model,
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		System:    system,
		Presets:   presets,
		Hooks:     hookSet,
	}))

	runner, err := agent.NewRunner(agent.RunnerConfig{
		Providers:       reg,
		Model:           model,
		Tools:           agent.ToolSetFromRegistry(toolReg),
		ToolDefinitions: toolReg.Definitions(),
	},
		agent.WithStreamOptions(ai.StreamOptions{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}),
		agent.WithMaxTurns(maxToolIterations),
		agent.WithSystem(system),
		agent.WithHooks(hookSet, hooks.HookMeta{}),
	)
	if err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
	}

	return &GoRunner{
		runner:       runner,
		reg:          reg,
		tools:        toolReg,
		lastActivity: time.Now(),
		log:          slog.With("component", "go_runner"),
	}, nil
}

// buildProviderRegistry creates the provider registry for the configured API.
func buildProviderRegistry(cfg GoRunnerConfig) (*providers.Registry, error) {
	reg, err := pluginproviders.BuildRegistry(cfg.API, pluginproviders.ProviderConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
	}
	return reg, nil
}

// buildToolRegistry creates the tool registry with core and extra tools.
func buildToolRegistry(cfg GoRunnerConfig) *tools.Registry {
	// Extract embedded tool binaries (idempotent, safe for concurrent calls).
	if err := embedded.EnsureTools(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	toolsBinDir := embedded.BinDir(config.AnnaHome())

	toolReg := tools.NewRegistry()

	// Core tools (read, bash, edit, write) via plugin registry.
	bc := plugintools.BuildContext{
		WorkDir:     cfg.WorkDir,
		UserDataDir: cfg.UserDataDir,
		AnnaHome:    cfg.AnnaHome,
		Workspace:   cfg.Workspace,
		ToolsBinDir: toolsBinDir,
	}
	for _, t := range plugintools.BuildCore(bc) {
		toolReg.Register(t)
	}

	// Extra tools (shared tools like memory, scheduler + plugin tools like webfetch).
	for _, t := range cfg.ExtraTools {
		toolReg.Register(t)
	}

	return toolReg
}

// buildAgentPresets extracts builtin skills and loads agent presets from filesystem.
func buildAgentPresets(cfg GoRunnerConfig) *agenttool.PresetRegistry {
	builtinSkillsDir := filepath.Join(config.AnnaHome(), "cache", "builtin-skills")
	if err := builtin.Extract(builtinSkillsDir); err != nil {
		slog.Warn("failed to extract builtin skills", "error", err)
	}
	return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
		Workspace:        cfg.Workspace,
		Cwd:              cfg.WorkDir,
		BuiltinSkillsDir: builtinSkillsDir,
	}))
}

// buildHookSet creates the hook set from configured hook plugins.
func buildHookSet(cfg GoRunnerConfig) *hooks.HookSet {
	if len(cfg.HookPlugins) > 0 {
		return hooks.NewHookSet(cfg.HookPlugins)
	}
	return nil
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

		if _, err := r.runner.Run(ctx, messages, func(e agent.LoopEvent) {
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

// convertLoopEvent bridges agent.LoopEvent to Event(s).
func convertLoopEvent(e agent.LoopEvent) []Event {
	switch e := e.(type) {
	case agent.AssistantDelta:
		if d, ok := e.Event.(ai.EventTextDelta); ok && d.Text != "" {
			return []Event{{Text: d.Text}}
		}

	case agent.AssistantFinished:
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

	case agent.ToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			Tool:   e.ToolCall.Name,
			Status: "running",
			Input:  summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
		}}}

	case agent.ToolFinished:
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

	case agent.AgentErrored:
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
