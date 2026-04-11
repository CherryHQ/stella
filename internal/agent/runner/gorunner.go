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
	internalsandbox "github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
	agenttool "github.com/vaayne/anna/plugins/tools/agent"
)

const maxToolIterations = 40

type (
	ProviderRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error)
	CoreToolsBuilder        func(plugintools.BuildContext) []tools.Tool
)

// GoRunnerConfig configures the Go runner.
type GoRunnerConfig struct {
	API            string // provider key: "anthropic", "openai"
	Model          string // e.g. "claude-sonnet-4-20250514"
	APIKey         string
	BaseURL        string // optional provider base URL override
	WorkDir        string // working directory for tool execution
	Workspace      string // workspace dir for skills/memory (e.g. ~/.anna/workspace)
	AnnaHome       string // anna home directory (e.g. ~/.anna)
	System         string // optional system prompt override (bypasses default prompt building)
	PromptSections []pkgplugins.SystemPromptSection
	ExtraTools     []tools.Tool       // additional tools to register
	UserDataDir    string             // per-user data directory for sandbox enforcement (empty = no sandbox)
	HookPlugins    []hooks.HookPlugin // hook plugins for the engine loop
	ToolLifecycle  *coreagent.ToolLifecycle
	CoreTools      CoreToolsBuilder
	Providers      ProviderRegistryBuilder
	Sandbox        config.SandboxConfig
	DisableSandbox bool // for testing: disable boxsh sandbox even on Linux/macOS
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
	backend       sandboxBackend

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

	reg, err := buildProviderRegistry(cfg)
	if err != nil {
		return nil, err
	}

	system := cfg.System
	if system == "" {
		system = BuildSystemPromptFromDB(context.Background(), DBPromptParams{
			AnnaHome:       cfg.AnnaHome,
			Workspace:      cfg.Workspace,
			Cwd:            cfg.WorkDir,
			UserDataDir:    cfg.UserDataDir,
			PromptSections: cfg.PromptSections,
		})
	}

	model := ai.Model{API: cfg.API, Name: cfg.Model}

	if err := prepareSandbox(ctx, cfg); err != nil {
		return nil, err
	}

	backend, err := resolveSandboxBackend(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
	}

	toolReg, err := buildToolRegistry(cfg, backend)
	if err != nil {
		if backend != nil {
			_ = backend.Close()
		}
		return nil, err
	}
	presets := buildAgentPresets(cfg)
	hookSet := buildHookSet(cfg)

	toolReg.Register(agenttool.NewAgentTool(agenttool.AgentConfig{
		Providers:     reg,
		Registry:      toolReg,
		Model:         model,
		APIKey:        cfg.APIKey,
		BaseURL:       cfg.BaseURL,
		System:        system,
		Presets:       presets,
		Hooks:         hookSet,
		ToolLifecycle: cfg.ToolLifecycle,
	}))

	streamOptions := ai.StreamOptions{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}
	runner, err := newAgentRunner(reg, toolReg, model, streamOptions, system, hookSet, cfg.ToolLifecycle)
	if err != nil {
		if backend != nil {
			_ = backend.Close()
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
		backend:       backend,
		lastActivity:  time.Now(),
		log:           slog.With("component", "go_runner"),
	}, nil
}

// createAndStartBackend creates and starts the boxsh shared backend.
func createAndStartBackend(ctx context.Context, cfg GoRunnerConfig) (*boxshclient.SharedBackend, error) {
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}

	backendCfg := boxshclient.BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   cfg.Workspace,
		UserDataDir: cfg.UserDataDir,
		Sandbox:     cfg.Sandbox,
		WorkDir:     cfg.WorkDir,
		ReadOnlyDirs: collectSandboxReadOnlyDirs(
			embedded.BinDir(annaHome),
			os.Getenv("PATH"),
		),
	}

	backend, err := boxshclient.NewSharedBackend(backendCfg)
	if err != nil {
		return nil, fmt.Errorf("create boxsh backend: %w", err)
	}

	if err := backend.Start(ctx, backendCfg); err != nil {
		return nil, fmt.Errorf("start boxsh backend: %w", err)
	}

	slog.Info("go runner using boxsh core tools",
		"component", "go_runner",
		"workspace", cfg.Workspace,
		"user_data_dir", cfg.UserDataDir,
		"work_dir", cfg.WorkDir,
		"network_mode", cfg.Sandbox.NetworkMode(),
		"readonly_dir_count", len(backendCfg.ReadOnlyDirs),
	)

	return backend, nil
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
func buildToolRegistry(cfg GoRunnerConfig, backend sandboxBackend) (*tools.Registry, error) {
	// Extract embedded tool binaries (idempotent, safe for concurrent calls).
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}
	if err := embedded.EnsureTools(annaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	toolsBinDir := embedded.BinDir(annaHome)

	toolReg := tools.NewRegistry()

	// Core tools (read, bash, edit, write) via plugin registry.
	if cfg.CoreTools == nil {
		return nil, fmt.Errorf("go runner: core tools builder is required")
	}
	bc := plugintools.BuildContext{
		WorkDir:     cfg.WorkDir,
		UserDataDir: cfg.UserDataDir,
		AnnaHome:    cfg.AnnaHome,
		Workspace:   cfg.Workspace,
		ToolsBinDir: toolsBinDir,
		Backend:     backend.Boxsh(),
		Sandbox:     backend.Runtime(),
	}
	coreToolsBuilder := CoreToolsBuilderWithBoxsh(cfg.CoreTools)
	for _, t := range coreToolsBuilder(bc) {
		toolReg.Register(t)
	}

	// Extra tools (shared tools like memory, scheduler + plugin tools like webfetch).
	for _, t := range cfg.ExtraTools {
		toolReg.Register(t)
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
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}
	builtinSkillsDir := filepath.Join(annaHome, "cache", "builtin-skills")
	if err := builtin.Extract(builtinSkillsDir); err != nil {
		slog.Warn("failed to extract builtin skills", "error", err)
	}
	return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
		Workspace:        cfg.Workspace,
		Cwd:              cfg.WorkDir,
		BuiltinSkillsDir: builtinSkillsDir,
	}))
}

func prepareSandbox(ctx context.Context, cfg GoRunnerConfig) error {
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}
	if err := embedded.EnsureTools(annaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	if cfg.DisableSandbox {
		return nil
	}
	if err := internalsandbox.Preflight(ctx, internalsandbox.PreflightConfig{
		AnnaHome:    annaHome,
		Workspace:   cfg.Workspace,
		UserDataDir: cfg.UserDataDir,
		Sandbox:     cfg.Sandbox,
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
// On Linux/macOS, it also checks the boxsh backend health.
func (r *GoRunner) Alive() bool {
	return r.backend.Alive()
}

// LastActivity returns the time of the last Chat call.
func (r *GoRunner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}

// SystemPrompt returns the runner's base system prompt before per-run overrides.
func (r *GoRunner) SystemPrompt() string { return r.system }

// Close shuts down any subprocess-backed tools and the boxsh backend.
func (r *GoRunner) Close() error {
	var errs []error

	if r.tools != nil {
		if err := r.tools.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := r.backend.Close(); err != nil {
		errs = append(errs, err)
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
