package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	plugintools "github.com/CherryHQ/stella/plugins/tools"
	agenttool "github.com/CherryHQ/stella/plugins/tools/agent"
	"github.com/CherryHQ/stella/resources"
)

// providerConfig groups LLM provider settings.
type providerConfig struct {
	API     string // provider key: "anthropic", "openai"
	Model   string // e.g. "claude-sonnet-4-20250514"
	APIKey  string
	BaseURL string // optional provider base URL override
	Builder ProviderRegistryBuilder
}

// runnerConfig configures the runner implementation.
type runnerConfig struct {
	Provider        providerConfig
	Sandbox         sandbox.Config
	System          string // optional system prompt override (bypasses default prompt building)
	PluginPrompts   []pkgplugins.SystemPromptSection
	PromptSections  []pkgplugins.SystemPromptSection
	ExtraTools      []tools.Tool // additional tools to register
	PluginTools     func(context.Context, plugintools.BuildContext) []tools.Tool
	HookPlugins     []hooks.HookPlugin // hook plugins for the engine loop
	ToolLifecycle   *coreagent.ToolLifecycle
	SubagentTimeout time.Duration // default wall-clock timeout per subagent (0 = 15m)
	ChatTimeout     time.Duration // wall-clock timeout per main agent chat turn (0 = 30m)
}

// runner implements Runner by calling LLM providers directly via agent.Runner.
type runner struct {
	runner        *coreagent.Runner
	reg           *providers.Registry
	tools         *tools.Registry
	model         ai.Model
	streamOptions ai.StreamOptions
	system        string
	hookSet       *hooks.HookSet
	toolLifecycle *coreagent.ToolLifecycle
	chatTimeout   time.Duration
	session       *sandbox.Session // runner-owned sandbox session lifecycle

	mu           sync.Mutex
	lastActivity time.Time
	activeCalls  int
	log          *slog.Logger
}

// newRunner creates a runner with built-in providers.
func newRunner(ctx context.Context, cfg runnerConfig) (*runner, error) {
	if cfg.Provider.API == "" {
		return nil, fmt.Errorf("runner: api is required")
	}
	if cfg.Provider.Model == "" {
		return nil, fmt.Errorf("runner: model is required")
	}
	if cfg.Provider.APIKey == "" {
		return nil, fmt.Errorf("runner: api_key is required")
	}
	if cfg.Sandbox.Paths.AgentRoot == "" {
		return nil, fmt.Errorf("runner: agent_root is required")
	}
	if cfg.Sandbox.Paths.UserRoot == "" {
		return nil, fmt.Errorf("runner: user_root is required")
	}

	paths, err := sandbox.ResolvePaths(cfg.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	reg, err := buildProviderRegistry(cfg)
	if err != nil {
		return nil, err
	}

	system := cfg.System

	model := ai.Model{API: cfg.Provider.API, Name: cfg.Provider.Model}

	session, err := sandbox.ResolveSession(ctx, cfg.Sandbox)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	if system == "" {
		system = prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
			StellaHome:     paths.StellaHome,
			AgentRoot:      paths.AgentRoot,
			ProjectRoot:    paths.ProjectRoot,
			UserRoot:       paths.UserRoot,
			PluginPrompts:  cfg.PluginPrompts,
			PromptSections: cfg.PromptSections,
			Host:           session.Session(),
		})
	}

	toolReg, err := buildToolRegistry(ctx, cfg, session)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, err
	}
	presets := buildAgentPresets(cfg)
	hookSet := buildHookSet(cfg)

	toolReg.Register(agenttool.NewAgentTool(agenttool.AgentConfig{
		Providers:      reg,
		Registry:       toolReg,
		Model:          model,
		APIKey:         cfg.Provider.APIKey,
		BaseURL:        cfg.Provider.BaseURL,
		System:         system,
		Presets:        presets,
		Hooks:          hookSet,
		ToolLifecycle:  cfg.ToolLifecycle,
		DefaultTimeout: cfg.SubagentTimeout,
	}))

	streamOptions := ai.StreamOptions{APIKey: cfg.Provider.APIKey, BaseURL: cfg.Provider.BaseURL}
	coreRunner, err := newAgentRunner(reg, toolReg, model, streamOptions, system, hookSet, cfg.ToolLifecycle)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, fmt.Errorf("runner: %w", err)
	}

	return &runner{
		runner:        coreRunner,
		reg:           reg,
		tools:         toolReg,
		model:         model,
		streamOptions: streamOptions,
		system:        system,
		hookSet:       hookSet,
		toolLifecycle: cfg.ToolLifecycle,
		chatTimeout:   cfg.ChatTimeout,
		session:       session,
		lastActivity:  time.Now(),
		log:           slog.With("component", "go_runner"),
	}, nil
}

func newAgentRunner(reg *providers.Registry, toolReg *tools.Registry, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle) (*coreagent.Runner, error) {
	toolSet := coreagent.ToolSetFromRegistry(toolReg)
	toolDefs := toolReg.Definitions()
	return newAgentRunnerWithTools(reg, model, streamOptions, system, hookSet, toolLifecycle, toolSet, toolDefs)
}

func newAgentRunnerWithTools(reg *providers.Registry, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle, toolSet coreagent.ToolSet, toolDefs []tools.Definition) (*coreagent.Runner, error) {
	return coreagent.NewRunner(coreagent.RunnerConfig{
		Providers:       reg,
		Model:           model,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
	},
		coreagent.WithStreamOptions(streamOptions),
		coreagent.WithSystem(system),
		coreagent.WithHooks(hookSet, hooks.HookMeta{}),
		coreagent.WithToolLifecycle(toolLifecycle),
	)
}

// buildProviderRegistry creates the provider registry for the configured API.
func buildProviderRegistry(cfg runnerConfig) (*providers.Registry, error) {
	if cfg.Provider.Builder == nil {
		return nil, fmt.Errorf("runner: provider registry builder is required")
	}
	reg, err := cfg.Provider.Builder(cfg.Provider.API, cfg.Provider.APIKey, cfg.Provider.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	return reg, nil
}

// buildToolRegistry creates the tool registry with core, builtin, and external tools.
func buildToolRegistry(ctx context.Context, cfg runnerConfig, session *sandbox.Session) (*tools.Registry, error) {
	paths, _ := sandbox.ResolvePaths(cfg.Sandbox)
	toolReg := tools.NewRegistry()

	// Core tools (read, bash, edit, write) are always provided by the active
	// sandbox session.

	toolsBinDir := resolveToolsBinDir(paths, config.SandboxBackendDocker)

	// Runtime capabilities are injected from the active runner session.
	bc := plugintools.BuildContext{
		Paths: pkgplugins.ToolPaths{
			UserRoot:    paths.UserRoot,
			ToolsBinDir: toolsBinDir,
			StellaHome:  paths.StellaHome,
			AgentRoot:   paths.AgentRoot,
			ProjectRoot: paths.ProjectRoot,
		},
		Runtime: session.Session(),
	}

	coreTools := buildSandboxCoreTools(session, bc)
	if len(coreTools) == 0 {
		return nil, fmt.Errorf("runner: sandbox backend unavailable: core tools require an active sandbox host")
	}

	// Sandbox core tools (bash/read/write/edit) route through the active
	// session and must win over any plugin tool of the same name. Plugin
	// versions run in the stella process, which would bypass the sandbox.
	coreNames := make(map[string]struct{}, len(coreTools))
	for _, t := range coreTools {
		coreNames[t.Definition().Name] = struct{}{}
		toolReg.Register(t)
	}

	registerNonCore := func(t tools.Tool) {
		name := t.Definition().Name
		if _, taken := coreNames[name]; taken {
			slog.Debug("skipping non-sandbox tool that collides with sandbox core",
				"component", "go_runner", "tool", name)
			return
		}
		toolReg.Register(t)
	}

	for _, t := range cfg.ExtraTools {
		registerNonCore(t)
	}
	if cfg.PluginTools != nil {
		for _, t := range cfg.PluginTools(ctx, bc) {
			registerNonCore(t)
		}
	}

	return toolReg, nil
}

func filterRunnerTools(reg *tools.Registry, excluded []string) (coreagent.ToolSet, []tools.Definition, error) {
	if len(excluded) == 0 {
		return coreagent.ToolSetFromRegistry(reg), reg.Definitions(), nil
	}
	blocked := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		if name == "" {
			continue
		}
		blocked[name] = struct{}{}
	}
	allowed := make([]string, 0, len(reg.Definitions()))
	for _, def := range reg.Definitions() {
		if _, skip := blocked[def.Name]; skip {
			continue
		}
		allowed = append(allowed, def.Name)
	}
	return coreagent.ToolSetFromRegistryFiltered(reg, allowed)
}

func buildAgentPresets(cfg runnerConfig) *agenttool.PresetRegistry {
	paths, _ := sandbox.ResolvePaths(cfg.Sandbox)
	if err := resources.ExtractSubAgents(stellaAgentsDir(paths)); err != nil {
		slog.Warn("failed to extract builtin agents", "error", err)
	}
	return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
		StellaHome:  paths.StellaHome,
		AgentRoot:   paths.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: paths.ProjectRoot,
	}))
}

// buildHookSet creates the hook set from configured hook plugins.
func buildHookSet(cfg runnerConfig) *hooks.HookSet {
	if len(cfg.HookPlugins) > 0 {
		return hooks.NewHookSet(cfg.HookPlugins)
	}
	return nil
}

const defaultChatTimeout = 30 * time.Minute

// ErrChatTimeout is returned when the main agent chat exceeds its wall-clock timeout.
var ErrChatTimeout = errors.New("chat timeout exceeded")

// Chat runs the Engine agent loop with the provided history and forwards events.
func (r *runner) Chat(ctx context.Context, history []ai.Message, message MessageContent) <-chan Event {
	out := make(chan Event, 100)

	timeout := r.chatTimeout
	if timeout <= 0 {
		timeout = defaultChatTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	r.mu.Lock()
	r.lastActivity = time.Now()
	r.activeCalls++
	r.mu.Unlock()

	go func() {
		defer cancel()
		defer close(out)
		defer func() {
			r.mu.Lock()
			r.activeCalls--
			r.lastActivity = time.Now()
			r.mu.Unlock()
		}()

		loopRunner := r.runner
		effectiveSystem := r.system
		if override, ok := SystemOverrideFromContext(ctx); ok && override != "" {
			effectiveSystem = override
		}
		excludedTools := ExcludedToolsFromContext(ctx)
		if effectiveSystem != r.system || len(excludedTools) > 0 {
			toolSet := coreagent.ToolSetFromRegistry(r.tools)
			toolDefs := r.tools.Definitions()
			if len(excludedTools) > 0 {
				filteredSet, filteredDefs, err := filterRunnerTools(r.tools, excludedTools)
				if err != nil {
					out <- Event{Err: fmt.Errorf("runner: %w", err)}
					return
				}
				toolSet = filteredSet
				toolDefs = filteredDefs
			}
			tempRunner, err := newAgentRunnerWithTools(r.reg, r.model, r.streamOptions, effectiveSystem, r.hookSet, r.toolLifecycle, toolSet, toolDefs)
			if err != nil {
				out <- Event{Err: fmt.Errorf("runner: %w", err)}
				return
			}
			loopRunner = tempRunner
		}

		// Inject session context into hook metadata so hooks can log it.
		loopRunner.SetHookMeta(hooks.HookMeta{
			SessionID: memory.SessionIDFromContext(ctx),
			UserID:    memory.UserIDFromContext(ctx),
			AgentID:   memory.AgentIDFromContext(ctx),
			Channel: func() string {
				channel, _ := ChannelFromContext(ctx)
				return channel
			}(),
		})

		// Inject progress nudges at milestone turns so the model can summarize
		// its state before the timeout fires.
		chatStart := time.Now()
		loopRunner.SetTurnNotify(func(turn int, _ time.Duration) *string {
			elapsed := time.Since(chatStart).Round(time.Second)
			var msg string
			switch turn {
			case 50:
				msg = fmt.Sprintf("You have been running for %s and completed 50 turns. Please report your current progress. If the user's request is not yet resolved, suggest alternative approaches.", elapsed)
			case 80:
				msg = fmt.Sprintf("You have been running for %s and completed 80 turns. Please report your progress again and consider whether a simpler approach could resolve the problem.", elapsed)
			case 100:
				msg = fmt.Sprintf("You have been running for %s and completed 100 turns. Please summarize the current state clearly and stop further attempts. Wait for the user's instructions before continuing.", elapsed)
			default:
				return nil
			}
			return &msg
		})

		messages := make([]ai.Message, len(history))
		copy(messages, history)
		switch m := message.(type) {
		case ai.UserMessage:
			messages = append(messages, m)
		default:
			messages = append(messages, ai.UserMessage{Content: message, Timestamp: time.Now()})
		}

		if _, err := loopRunner.Run(ctx, messages, func(e coreagent.LoopEvent) {
			for _, evt := range convertLoopEvent(e) {
				out <- evt
			}
		}); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				err = fmt.Errorf("%w: %w", ErrChatTimeout, err)
			}
			out <- Event{Err: err}
		}

		if r.session != nil {
			if err := r.session.Sync(); err != nil {
				slog.Warn("runner: sync session after chat", "error", err)
			}
		}
	}()

	return out
}

// Alive reports whether the runner is healthy.
// Delegates to the session's lifecycle state.
func (r *runner) Alive() bool {
	if r.session == nil {
		return false
	}
	return r.session.Alive()
}

// LastActivity returns the time of the last Chat call.
func (r *runner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}

// Busy reports whether a Chat call is currently in flight.
func (r *runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeCalls > 0
}

// SystemPrompt returns the runner's base system prompt before per-run overrides.
func (r *runner) SystemPrompt() string { return r.system }

// Close shuts down any subprocess-backed tools and the sandbox session.
// Guarantees cleanup of session resources regardless of state.
func (r *runner) Close() error {
	var errs []error

	if r.tools != nil {
		if err := r.tools.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if r.session != nil {
		if err := r.session.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// convertLoopEvent bridges agent.LoopEvent to Event(s).
func convertLoopEvent(e coreagent.LoopEvent) []Event {
	switch e := e.(type) {
	case coreagent.AssistantDelta:
		if d, ok := e.Event.(ai.EventTextDelta); ok && d.Text != "" {
			return []Event{{Text: d.Text}}
		}

	case coreagent.AssistantFinished:
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

	case coreagent.ToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			Tool:   e.ToolCall.Name,
			Status: "running",
			Input:  summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
		}}}

	case coreagent.ToolFinished:
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

	case coreagent.AgentErrored:
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
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "skills":
		action, _ := args["action"].(string)
		if name, _ := args["name"].(string); name != "" {
			return action + " " + name
		}
		if src, _ := args["source"].(string); src != "" {
			return action + " " + src
		}
		return action
	case "memory":
		action, _ := args["action"].(string)
		return action
	case "agent":
		if tasks, ok := args["tasks"].([]any); ok && len(tasks) > 0 {
			return fmt.Sprintf("%d task(s)", len(tasks))
		}
	}
	return ""
}
