package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/runner/builtin"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	boxshsandbox "github.com/vaayne/anna/plugins/sandbox/boxsh"
	plugintools "github.com/vaayne/anna/plugins/tools"
	agenttool "github.com/vaayne/anna/plugins/tools/agent"
)

const maxToolIterations = 40

type ProviderRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error)

// GoRunnerConfig configures the Go runner.
type GoRunnerConfig struct {
	API            string // provider key: "anthropic", "openai"
	Model          string // e.g. "claude-sonnet-4-20250514"
	APIKey         string
	BaseURL        string // optional provider base URL override
	WorkDir        string // working directory for tool execution; defaults to UserRoot when empty
	AgentRoot      string // agent root directory
	AnnaHome       string // anna home directory (e.g. ~/.anna)
	System         string // optional system prompt override (bypasses default prompt building)
	PromptSections []pkgplugins.SystemPromptSection
	ExtraTools     []tools.Tool // additional tools to register
	PluginTools    func(context.Context, plugintools.BuildContext) []tools.Tool
	ToolRuntime    pkgplugins.ToolRuntime
	UserRoot       string             // required per-user root used by prompts, skills, and sandbox execution
	HookPlugins    []hooks.HookPlugin // hook plugins for the engine loop
	ToolLifecycle  *coreagent.ToolLifecycle
	Providers      ProviderRegistryBuilder
	Sandbox        config.SandboxConfig
}

// GoRunner implements Runner by calling LLM providers directly via agent.Runner.
type GoRunner struct {
	runner        *coreagent.Runner
	reg           *providers.Registry
	tools         *tools.Registry
	model         ai.Model
	streamOptions ai.StreamOptions
	system        string
	hookSet       *hooks.HookSet
	toolLifecycle *coreagent.ToolLifecycle
	session       *runnerSession // runner-owned sandbox session lifecycle

	mu           sync.Mutex
	lastActivity time.Time
	log          *slog.Logger
}

// NewGoRunner creates a Go runner with built-in providers.
func NewGoRunner(ctx context.Context, cfg GoRunnerConfig) (*GoRunner, error) {
	if cfg.API == "" {
		return nil, fmt.Errorf("go runner: api is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("go runner: model is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("go runner: api_key is required")
	}
	if cfg.AgentRoot == "" {
		return nil, fmt.Errorf("go runner: agent_root is required")
	}
	if cfg.UserRoot == "" {
		return nil, fmt.Errorf("go runner: user_root is required")
	}

	paths := resolveRunnerPaths(cfg)

	reg, err := buildProviderRegistry(cfg)
	if err != nil {
		return nil, err
	}

	system := cfg.System

	model := ai.Model{API: cfg.API, Name: cfg.Model}

	if err := prepareSandbox(ctx, cfg); err != nil {
		return nil, err
	}

	session, err := resolveSession(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
	}
	if cfg.ToolRuntime == nil {
		cfg.ToolRuntime = toolRuntimeFromHost(session.Host())
	}

	if system == "" {
		system = BuildSystemPromptFromDB(context.Background(), DBPromptParams{
			AnnaHome:       paths.AnnaHome,
			AgentRoot:      paths.AgentRoot,
			UserRoot:       paths.UserRoot,
			PromptSections: cfg.PromptSections,
			Host:           session.Host(),
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
		Providers: reg,
		Registry:  toolReg,
		Model:     model,
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		System:    system,
		Presets:   presets,
		PresetLoader: func(projectRoot string) *agenttool.PresetRegistry {
			return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
				AnnaHome:         paths.AnnaHome,
				AgentRoot:        paths.AgentRoot,
				UserRoot:         paths.UserRoot,
				ProjectRoot:      projectRoot,
				BuiltinSkillsDir: paths.builtinSkillsDir(),
				Runtime:          cfg.ToolRuntime,
			}))
		},
		Hooks:         hookSet,
		ToolLifecycle: cfg.ToolLifecycle,
	}))

	streamOptions := ai.StreamOptions{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}
	runner, err := newAgentRunner(reg, toolReg, model, streamOptions, system, hookSet, cfg.ToolLifecycle)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, fmt.Errorf("go runner: %w", err)
	}

	return &GoRunner{
		runner:        runner,
		reg:           reg,
		tools:         toolReg,
		model:         model,
		streamOptions: streamOptions,
		system:        system,
		hookSet:       hookSet,
		toolLifecycle: cfg.ToolLifecycle,
		session:       session,
		lastActivity:  time.Now(),
		log:           slog.With("component", "go_runner"),
	}, nil
}

func newAgentRunner(reg *providers.Registry, toolReg *tools.Registry, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle) (*coreagent.Runner, error) {
	return coreagent.NewRunner(coreagent.RunnerConfig{
		Providers:       reg,
		Model:           model,
		Tools:           coreagent.ToolSetFromRegistry(toolReg),
		ToolDefinitions: toolReg.Definitions(),
	},
		coreagent.WithStreamOptions(streamOptions),
		coreagent.WithMaxTurns(maxToolIterations),
		coreagent.WithSystem(system),
		coreagent.WithHooks(hookSet, hooks.HookMeta{}),
		coreagent.WithToolLifecycle(toolLifecycle),
	)
}

// buildProviderRegistry creates the provider registry for the configured API.
func buildProviderRegistry(cfg GoRunnerConfig) (*providers.Registry, error) {
	if cfg.Providers == nil {
		return nil, fmt.Errorf("go runner: provider registry builder is required")
	}
	reg, err := cfg.Providers(cfg.API, cfg.APIKey, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
	}
	return reg, nil
}

// buildToolRegistry creates the tool registry with core and extra tools.
func buildToolRegistry(ctx context.Context, cfg GoRunnerConfig, session *runnerSession) (*tools.Registry, error) {
	// Extract embedded tool binaries (idempotent, safe for concurrent calls).
	paths := resolveRunnerPaths(cfg)
	if err := embedded.EnsureTools(paths.AnnaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}

	toolReg := tools.NewRegistry()

	// Core tools (read, bash, edit, write) are always provided by the active
	// sandbox session.

	// Runtime capabilities are injected from the active runner session.
	bc := plugintools.BuildContext{
		WorkDir:     paths.WorkDir,
		ProjectRoot: "",
		UserRoot:    paths.UserRoot,
		AnnaHome:    paths.AnnaHome,
		HomeDir:     paths.UserHome,
		AgentRoot:   paths.AgentRoot,
		ToolsBinDir: paths.toolsBinDir(),
		Runtime:     cfg.ToolRuntime,
	}

	coreTools := buildSandboxCoreTools(session, bc)
	if len(coreTools) == 0 {
		return nil, fmt.Errorf("go runner: sandbox backend unavailable: core tools require an active sandbox host")
	}
	for _, t := range coreTools {
		toolReg.Register(t)
	}

	// Extra tools (shared tools like memory, scheduler + plugin tools like webfetch).
	for _, t := range cfg.ExtraTools {
		toolReg.Register(t)
	}
	if cfg.PluginTools != nil {
		for _, t := range cfg.PluginTools(ctx, bc) {
			toolReg.Register(t)
		}
	}

	return toolReg, nil
}

// buildAgentPresets extracts builtin skills and loads agent presets from filesystem.
func collectSandboxReadOnlyDirs(toolsBinDir, pathEnv string) []string {
	dirs := []string{toolsBinDir}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func buildAgentPresets(cfg GoRunnerConfig) *agenttool.PresetRegistry {
	paths := resolveRunnerPaths(cfg)
	if err := builtin.Extract(paths.builtinSkillsDir()); err != nil {
		slog.Warn("failed to extract builtin skills", "error", err)
	}
	return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
		AnnaHome:         paths.AnnaHome,
		AgentRoot:        paths.AgentRoot,
		UserRoot:         paths.UserRoot,
		ProjectRoot:      "",
		BuiltinSkillsDir: paths.builtinSkillsDir(),
		Runtime:          cfg.ToolRuntime,
	}))
}

func prepareSandbox(ctx context.Context, cfg GoRunnerConfig) error {
	paths := resolveRunnerPaths(cfg)
	if err := embedded.EnsureTools(paths.AnnaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	if cfg.Sandbox.BackendName() == config.SandboxBackendLocal {
		return nil
	}
	if err := boxshsandbox.Preflight(ctx, boxshsandbox.PreflightConfig{
		AnnaHome:    paths.AnnaHome,
		Workspace:   paths.AgentRoot,
		UserDataDir: paths.UserRoot,
		Network: boxshsandbox.NetworkConfig{
			Mode:      cfg.Sandbox.Network.Mode,
			Allowlist: cfg.Sandbox.Network.Allowlist,
		},
	}); err != nil {
		return fmt.Errorf("go runner: %w", err)
	}
	return nil
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

		loopRunner := r.runner
		if override, ok := SystemOverrideFromContext(ctx); ok && override != r.system {
			tempRunner, err := newAgentRunner(r.reg, r.tools, r.model, r.streamOptions, override, r.hookSet, r.toolLifecycle)
			if err != nil {
				out <- Event{Err: fmt.Errorf("go runner: %w", err)}
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

		messages := make([]ai.Message, len(history))
		copy(messages, history)
		messages = append(messages, ai.UserMessage{Content: message})

		if _, err := loopRunner.Run(ctx, messages, func(e coreagent.LoopEvent) {
			for _, evt := range convertLoopEvent(e) {
				out <- evt
			}
		}); err != nil {
			out <- Event{Err: err}
		}
	}()

	return out
}

// Alive reports whether the runner is healthy.
// Delegates to the session's lifecycle state.
func (r *GoRunner) Alive() bool {
	if r.session == nil {
		return false
	}
	return r.session.Alive()
}

// LastActivity returns the time of the last Chat call.
func (r *GoRunner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}

// SystemPrompt returns the runner's base system prompt before per-run overrides.
func (r *GoRunner) SystemPrompt() string { return r.system }

// Close shuts down any subprocess-backed tools and the sandbox session.
// Guarantees cleanup of session resources regardless of state.
func (r *GoRunner) Close() error {
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
	}
	return ""
}
