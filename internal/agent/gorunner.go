package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	builtinres "github.com/vaayne/anna/internal/resources"
	"github.com/vaayne/anna/internal/resources/binaries"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	plugintools "github.com/vaayne/anna/plugins/tools"
	agenttool "github.com/vaayne/anna/plugins/tools/agent"
)

// maxAgentLoopTurns caps one runner invocation's model/tool decision loop.
// A single turn may include multiple tool calls; the limit is on loop rounds,
// not individual tool executions.
const maxAgentLoopTurns = 50

// VaultEnvLoader loads decrypted vault entries for a user as a name→value map.
// It is a subset of vault.Service and is defined here to avoid a circular
// import between the runner and vault packages.
type VaultEnvLoader interface {
	LoadEnv(ctx context.Context, userID int64) (map[string]string, error)
}

// GoRunnerConfig configures the Go runner.
type GoRunnerConfig struct {
	API              string // provider key: "anthropic", "openai"
	Model            string // e.g. "claude-sonnet-4-20250514"
	APIKey           string
	BaseURL          string // optional provider base URL override
	AgentRoot        string // agent root directory
	AnnaHome         string // anna home directory (e.g. ~/.anna)
	ProjectRoot      string // optional project root for project-aware tools and prompt/context loading
	System           string // optional system prompt override (bypasses default prompt building)
	PluginPrompts    []pkgplugins.SystemPromptSection
	PromptSections   []pkgplugins.SystemPromptSection
	ExtraTools       []tools.Tool // additional tools to register
	PluginTools      func(context.Context, plugintools.BuildContext) []tools.Tool
	SessionEnvSpecs  []pkgplugins.SessionEnvSpec
	UserRoot         string             // required per-user root used by prompts, skills, and sandbox execution
	HookPlugins      []hooks.HookPlugin // hook plugins for the engine loop
	ToolLifecycle    *coreagent.ToolLifecycle
	Providers        ProviderRegistryBuilder
	Sandbox          config.SandboxConfig
	SandboxBackendFn func(ctx context.Context) string // resolves active backend at session time; overrides Sandbox.Backend
	UserID           int64                            // auth user ID; used for vault secret injection
	VaultEnvLoader   VaultEnvLoader                   // optional; if set, vault secrets are injected into sandbox env
	TokenService     *auth.TokenService               // optional; if set, ensures ANNA_TOKEN before vault env injection
	TokenManager     *oauth.TokenManager              // optional; if set, runtime OAuth tokens are injected into sandbox env
	SubagentTimeout  time.Duration                    // default wall-clock timeout per subagent (0 = 15m)
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
	activeCalls  int
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
	if _, err := resolveSandboxPaths(cfg); err != nil {
		return nil, fmt.Errorf("go runner: %w", err)
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
	if system == "" {
		system = BuildSystemPromptFromDB(context.Background(), DBPromptParams{
			AnnaHome:       paths.AnnaHome,
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
		APIKey:         cfg.APIKey,
		BaseURL:        cfg.BaseURL,
		System:         system,
		Presets:        presets,
		Hooks:          hookSet,
		ToolLifecycle:  cfg.ToolLifecycle,
		DefaultTimeout: cfg.SubagentTimeout,
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
		coreagent.WithMaxTurns(maxAgentLoopTurns),
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

// buildToolRegistry creates the tool registry with core, builtin, and external tools.
func buildToolRegistry(ctx context.Context, cfg GoRunnerConfig, session *runnerSession) (*tools.Registry, error) {
	// Extract embedded tool binaries (idempotent, safe for concurrent calls).
	paths := resolveRunnerPaths(cfg)
	if err := binaries.EnsureTools(paths.AnnaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}

	toolReg := tools.NewRegistry()

	// Core tools (read, bash, edit, write) are always provided by the active
	// sandbox session.

	toolsBinDir := resolveToolsBinDir(paths, config.SandboxBackendDocker)

	// Runtime capabilities are injected from the active runner session.
	bc := plugintools.BuildContext{
		Paths: pkgplugins.ToolPaths{
			UserRoot:    paths.UserRoot,
			ToolsBinDir: toolsBinDir,
			AnnaHome:    paths.AnnaHome,
			AgentRoot:   paths.AgentRoot,
			ProjectRoot: paths.ProjectRoot,
		},
		Runtime: session.Session(),
	}

	coreTools := buildSandboxCoreTools(session, bc)
	if len(coreTools) == 0 {
		return nil, fmt.Errorf("go runner: sandbox backend unavailable: core tools require an active sandbox host")
	}

	// Sandbox core tools (bash/read/write/edit) route through the active
	// session and must win over any plugin tool of the same name. Plugin
	// versions run in the anna process, which would bypass the sandbox.
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

func buildAgentPresets(cfg GoRunnerConfig) *agenttool.PresetRegistry {
	paths := resolveRunnerPaths(cfg)
	if err := builtinres.ExtractSubAgents(paths.annaAgentsDir()); err != nil {
		slog.Warn("failed to extract builtin agents", "error", err)
	}
	return agenttool.NewPresetRegistry(agenttool.LoadAgentPresets(agenttool.LoadAgentPresetsConfig{
		AnnaHome:    paths.AnnaHome,
		AgentRoot:   paths.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: paths.ProjectRoot,
	}))
}

func prepareSandbox(ctx context.Context, cfg GoRunnerConfig) error {
	paths := resolveRunnerPaths(cfg)
	if err := binaries.EnsureTools(paths.AnnaHome); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	if err := builtinres.EnsureBuiltinSkills(paths.annaSkillsDir()); err != nil {
		slog.Warn("failed to extract builtin skills", "error", err)
	}

	backend := config.SandboxBackendLocal
	if cfg.SandboxBackendFn != nil {
		if name := cfg.SandboxBackendFn(ctx); name != "" {
			backend = name
		}
	}

	if backend == config.SandboxBackendDocker {
		dockerCfg, err := resolveDockerConfig()
		if err != nil {
			return fmt.Errorf("go runner: %w", err)
		}
		cleanupOrphanedDockerContainers(ctx, dockerCfg.TranslateToDaemonPath(paths.AnnaHome))
		if err := dockerplugin.Preflight(ctx, dockerplugin.PreflightConfig{
			AnnaHome: paths.AnnaHome,
			Docker:   dockerCfg,
		}); err != nil {
			if config.SandboxDockerImageIsDev() {
				return fmt.Errorf("go runner: %w (run `mise run sandbox:docker:build` to build the local %q image)", err, config.SandboxDockerImage())
			}
			return fmt.Errorf("go runner: docker not available; install and start Docker Desktop or the docker daemon: %w", err)
		}
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
	r.activeCalls++
	r.mu.Unlock()

	go func() {
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
					out <- Event{Err: fmt.Errorf("go runner: %w", err)}
					return
				}
				toolSet = filteredSet
				toolDefs = filteredDefs
			}
			tempRunner, err := newAgentRunnerWithTools(r.reg, r.model, r.streamOptions, effectiveSystem, r.hookSet, r.toolLifecycle, toolSet, toolDefs)
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
			out <- Event{Err: err}
		}

		if r.session != nil {
			if err := r.session.Sync(); err != nil {
				slog.Warn("go runner: sync session after chat", "error", err)
			}
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

// Busy reports whether a Chat call is currently in flight.
func (r *GoRunner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeCalls > 0
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
