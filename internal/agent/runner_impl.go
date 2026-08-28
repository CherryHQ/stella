package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agenterr"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/vision"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// providerConfig groups LLM provider settings.
type providerConfig struct {
	ProviderID string   // canonical provider row ID used in qualified model refs
	API        string   // provider adapter type: "anthropic", "openai"
	Model      string   // e.g. "claude-sonnet-4-20250514"
	Input      []string // declared model input modalities, e.g. ["text", "image"]; nil when undeclared
	Cost       ai.ModelCost
	APIKey     string
	BaseURL    string // optional provider base URL override
	Builder    ProviderStreamBuilder
}

// runnerConfig configures the runner implementation.
type runnerConfig struct {
	NoCapabilities       bool // guest mode: empty tool registry, no hooks or media
	Provider             providerConfig
	Thinking             ai.ThinkingLevel
	Sandbox              sandbox.Config
	System               string // optional system prompt override (bypasses default prompt building)
	Sections             []pkgplugins.SystemPromptSection
	BuiltinTools         []BuiltinTool
	BuiltinParams        RunnerParams
	DisabledSkillRefs    []string
	PerRunTools          []tools.Tool
	SkillRevisionReader  skillstool.RuntimeReader
	ProjectSkillSnapshot *skillstool.ProjectSnapshot
	SkillReadAuthorizer  skillstool.SkillReadAuthorizer
	PluginView           pkgplugins.SessionPluginView
	MCPToolProvider      MCPToolProvider
	ToolOverrideFetcher  ToolOverrideFetcher
	PluginTools          func(context.Context, pkgplugins.ToolBuildContext) []tools.Tool
	HookPlugins          []hooks.HookPlugin // hook plugins for the engine loop
	ToolLifecycle        *coreagent.ToolLifecycle
	DelegateRunner       delegatetool.SessionRunner
	DelegateTimeout      time.Duration // default wall-clock timeout per delegate (0 = 15m)
	ChatTimeout          time.Duration // wall-clock timeout per main agent chat turn (0 = 30m)
	CanonicalImages      *coreagent.CanonicalImageConfig
	Vision               *vision.Service // auxiliary vision service for view_image text routing
	Cleanup              func() error
	ToolMode             coreagent.ToolMode
	CodeToolSurface      coreagent.CodeToolSurface
}

// runner implements Runner by calling LLM providers directly via agent.Runner.
type runner struct {
	runner          *coreagent.Runner
	stream          providers.StreamFunc
	tools           *tools.Registry
	delegateTool    *delegatetool.DelegateTool
	model           ai.Model
	streamOptions   ai.StreamOptions
	toolMode        coreagent.ToolMode
	codeToolSurface coreagent.CodeToolSurface
	system          string
	hookSet         *hooks.HookSet
	toolLifecycle   *coreagent.ToolLifecycle
	canonicalImages *coreagent.CanonicalImageConfig
	chatTimeout     time.Duration
	session         pkgsandbox.Session // runner-owned sandbox session lifecycle
	noCapabilities  bool               // guest runner intentionally has no sandbox session
	sandboxCfg      sandbox.Config     // retained to refresh OAuth-derived env on long-lived runners
	cleanup         func() error

	mu           sync.Mutex
	lastActivity time.Time
	activeCalls  int
	log          *slog.Logger
}

// newRunner creates a runner with built-in providers.
func newRunner(ctx context.Context, cfg runnerConfig) (*runner, error) {
	stream, err := buildStreamFunc(cfg)
	if err != nil {
		return nil, err
	}

	systemPrompt := cfg.System

	providerID := cfg.Provider.ProviderID
	if providerID == "" {
		providerID = cfg.Provider.API
	}
	model := ai.Model{ID: cfg.Provider.Model, API: cfg.Provider.API, Name: cfg.Provider.Model, Provider: providerID, BaseURL: cfg.Provider.BaseURL, Input: cfg.Provider.Input, Cost: cfg.Provider.Cost}

	var session pkgsandbox.Session
	if !cfg.NoCapabilities {
		// Propagate the turn budget into the sandbox config so both initial OAuth env
		// injection and per-turn refresh size their min-validity to the actual chat
		// timeout (#722). cfg is a value copy, so this stays local to this runner.
		cfg.Sandbox.ChatTimeout = cfg.ChatTimeout

		session, err = sandbox.ResolveSession(ctx, cfg.Sandbox)
		if err != nil {
			return nil, fmt.Errorf("runner: %w", err)
		}
	}

	if systemPrompt == "" {
		systemPrompt = prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{Sections: cfg.Sections, Session: session, CodeMode: cfg.ToolMode == coreagent.ToolModeCode})
	}

	toolReg, hookSet, delegateTool, err := buildToolRegistry(ctx, cfg, session, stream, model, systemPrompt)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, err
	}

	streamOptions := ai.StreamOptions{Reasoning: cfg.Thinking}
	toolMode := cfg.ToolMode
	if toolMode == "" {
		toolMode = coreagent.ToolModeNative
	}
	coreRunner, err := newAgentRunner(stream, toolReg, model, streamOptions, systemPrompt, hookSet, cfg.ToolLifecycle, cfg.CanonicalImages, toolMode, cfg.CodeToolSurface)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, fmt.Errorf("runner: %w", err)
	}

	return &runner{
		runner:          coreRunner,
		stream:          stream,
		tools:           toolReg,
		delegateTool:    delegateTool,
		model:           model,
		streamOptions:   streamOptions,
		toolMode:        toolMode,
		codeToolSurface: cfg.CodeToolSurface,
		system:          systemPrompt,
		hookSet:         hookSet,
		toolLifecycle:   cfg.ToolLifecycle,
		canonicalImages: cfg.CanonicalImages,
		cleanup:         cfg.Cleanup,
		chatTimeout:     cfg.ChatTimeout,
		session:         session,
		noCapabilities:  cfg.NoCapabilities,
		sandboxCfg:      cfg.Sandbox,
		lastActivity:    time.Now(),
		log:             slog.With("component", "go_runner"),
	}, nil
}

func newAgentRunner(stream providers.StreamFunc, toolReg *tools.Registry, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle, canonicalImages *coreagent.CanonicalImageConfig, toolMode coreagent.ToolMode, codeToolSurface coreagent.CodeToolSurface) (*coreagent.Runner, error) {
	toolSet := coreagent.ToolSetFromRegistry(toolReg)
	toolDefs := toolReg.Definitions()
	return newAgentRunnerWithTools(stream, model, streamOptions, system, hookSet, toolLifecycle, canonicalImages, toolSet, toolDefs, toolMode, codeToolSurface)
}

func newAgentRunnerWithTools(stream providers.StreamFunc, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle, canonicalImages *coreagent.CanonicalImageConfig, toolSet coreagent.ToolSet, toolDefs []tools.Definition, toolMode coreagent.ToolMode, codeToolSurface coreagent.CodeToolSurface) (*coreagent.Runner, error) {
	opts := []coreagent.Option{
		coreagent.WithStreamOptions(streamOptions),
		coreagent.WithSystem(system),
		coreagent.WithHooks(hookSet, hooks.HookMeta{}),
		coreagent.WithToolLifecycle(toolLifecycle),
		coreagent.WithToolMode(toolMode),
		coreagent.WithCodeToolSurface(codeToolSurface),
	}
	if canonicalImages != nil {
		opts = append(opts, coreagent.WithCanonicalImages(*canonicalImages))
	}
	return coreagent.NewRunner(coreagent.RunnerConfig{
		Stream:          stream,
		Model:           model,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
	}, opts...)
}

// buildStreamFunc creates the stream function for the configured API.
func buildStreamFunc(cfg runnerConfig) (providers.StreamFunc, error) {
	if cfg.Provider.API == "" {
		return nil, fmt.Errorf("runner: api is required")
	}
	if cfg.Provider.Model == "" {
		return nil, fmt.Errorf("runner: model is required")
	}
	if cfg.Provider.APIKey == "" {
		return nil, fmt.Errorf("runner: api_key is required")
	}
	if cfg.Provider.Builder == nil {
		return nil, fmt.Errorf("runner: provider stream builder is required")
	}
	stream, err := cfg.Provider.Builder(cfg.Provider.API, cfg.Provider.APIKey, cfg.Provider.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	return stream, nil
}

// Tool sources, used to name both sides of a tool-name collision in the error
// the runner build fails with.
const (
	toolSourceCore    = "core"
	toolSourceBuiltin = "builtin"
	toolSourcePerRun  = "per-run"
	toolSourceMCP     = "mcp"
	toolSourcePlugin  = "plugin"
)

// toolCandidate is a non-core tool awaiting the override filter, carrying where
// it came from so a duplicate name can be attributed.
type toolCandidate struct {
	tool   tools.Tool
	source string
}

// buildToolRegistry creates the tool registry with core, builtin, and external tools.
func buildToolRegistry(ctx context.Context, cfg runnerConfig, session pkgsandbox.Session, stream providers.StreamFunc, model ai.Model, systemPrompt string) (*tools.Registry, *hooks.HookSet, *delegatetool.DelegateTool, error) {
	toolReg := tools.NewRegistry()
	if cfg.NoCapabilities {
		return toolReg, nil, nil, nil
	}

	// Core tools are provided by the active sandbox session.

	// Runtime capabilities are injected from the active runner session.
	bc := pkgplugins.ToolBuildContext{
		Runtime: session,
	}

	coreTools := buildSandboxCoreTools(session, cfg.Sandbox.SessionSecretValues, cfg.Vision)
	if len(coreTools) == 0 {
		return nil, nil, nil, fmt.Errorf("runner: sandbox backend unavailable: core tools require an active sandbox host")
	}

	// Sandbox core tools route through the active session and must win over any
	// process-local tool of the same name, which would bypass sandbox policy.
	sourceByName := make(map[string]string, len(coreTools))
	for _, t := range coreTools {
		if err := toolReg.Register(t); err != nil {
			return nil, nil, nil, fmt.Errorf("runner: register core tool: %w", err)
		}
		sourceByName[t.Definition().Name] = toolSourceCore
	}

	var nonCoreCandidates []toolCandidate
	registerNonCore := func(source string, t tools.Tool) {
		name := t.Definition().Name
		// Check the complete reservation set, not only core tools registered in
		// this runner. Legacy core names remain reserved even when no runtime tool
		// uses them.
		if IsCoreToolName(name) {
			slog.Debug("skipping non-core tool with reserved core name",
				"component", "go_runner", "tool", name, "reason", "reserved core tool name")
			return
		}
		nonCoreCandidates = append(nonCoreCandidates, toolCandidate{tool: t, source: source})
	}

	// Names of every builtin the deployment ships, available or not. Overrides
	// naming one of these are current rows for a tool this run cannot use, not
	// stale rows, so they must not be reported as orphans below.
	knownBuiltinNames := make(map[string]struct{}, len(cfg.BuiltinTools))
	for _, entry := range cfg.BuiltinTools {
		if definition, ok := entry.Definition(); ok {
			knownBuiltinNames[definition.Name] = struct{}{}
		}
	}

	for _, entry := range cfg.BuiltinTools {
		if entry.Available != nil {
			available, err := entry.Available(ctx, cfg.BuiltinParams)
			if err != nil {
				definition, _ := entry.Definition()
				return nil, nil, nil, fmt.Errorf("runner: resolve availability for builtin tool %q: %w", definition.Name, err)
			}
			if !available {
				continue
			}
		}
		if (entry.Tool == nil) == (entry.Build == nil) {
			return nil, nil, nil, fmt.Errorf("runner: builtin tool requires exactly one of Tool or Build")
		}
		if entry.Build != nil && entry.Spec.Name == "" {
			return nil, nil, nil, fmt.Errorf("runner: runtime-built builtin tool requires a static definition")
		}
		tool := entry.Tool
		if entry.Build != nil {
			var err error
			tool, err = entry.Build(bc)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("runner: build builtin tool: %w", err)
			}
			if tool == nil {
				return nil, nil, nil, fmt.Errorf("runner: built builtin tool is nil")
			}
			if definition := tool.Definition(); definition.Name != entry.Spec.Name {
				return nil, nil, nil, fmt.Errorf("runner: built builtin tool name %q does not match static definition %q", definition.Name, entry.Spec.Name)
			}
		}
		registerNonCore(toolSourceBuiltin, tool)
	}
	for _, t := range cfg.PerRunTools {
		registerNonCore(toolSourcePerRun, t)
	}
	skillsTool, err := skillstool.NewTool(cfg.SkillRevisionReader, session, cfg.SkillReadAuthorizer)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("runner: build skills tool: %w", err)
	}
	registerNonCore(toolSourceBuiltin, skillsTool.
		WithProjectSnapshot(cfg.ProjectSkillSnapshot).
		WithPluginVisibility(cfg.PluginView.RegisteredPluginIDs, cfg.PluginView.EnabledPluginIDs).
		WithAgentSkillPolicy(cfg.DisabledSkillRefs))
	if cfg.MCPToolProvider != nil {
		for _, t := range cfg.MCPToolProvider.ToolsForContext(ctx, cfg.BuiltinParams.UserID, cfg.BuiltinParams.AgentID) {
			registerNonCore(toolSourceMCP, t)
		}
	}
	if cfg.PluginTools != nil {
		for _, t := range cfg.PluginTools(ctx, bc) {
			registerNonCore(toolSourcePlugin, t)
		}
	}

	// Settle name ownership before any override is consulted. A plugin that
	// claims a builtin's name has to fail the build now: deferring the check to
	// the enabled set would let the grab sit dormant and detonate on the day an
	// operator re-enables the builtin it shadowed.
	for _, c := range nonCoreCandidates {
		name := c.tool.Definition().Name
		if prior, taken := sourceByName[name]; taken {
			return nil, nil, nil, fmt.Errorf("runner: %s tool %q collides with the %s tool of the same name",
				c.source, name, prior)
		}
		sourceByName[name] = c.source
	}

	hookSet := buildHookSet(cfg)
	delegateTool := delegatetool.NewDelegateTool(delegatetool.DelegateConfig{
		Stream:         stream,
		Registry:       toolReg,
		Model:          model,
		System:         systemPrompt,
		Presets:        buildDelegatePresets(cfg, session),
		Hooks:          hookSet,
		ToolLifecycle:  cfg.ToolLifecycle,
		SessionRunner:  cfg.DelegateRunner,
		DefaultTimeout: cfg.DelegateTimeout,
	})
	// Keep the internal delegate adapter for session.create/send preset execution.
	// It is intentionally absent from the model-facing registry.

	var overrides []ToolOverride
	if cfg.ToolOverrideFetcher != nil {
		rows, err := cfg.ToolOverrideFetcher(ctx, cfg.BuiltinParams.UserID, cfg.BuiltinParams.AgentID)
		if err != nil {
			// Defaulting to "visible" here would hand the model every tool an
			// administrator had switched off, for as long as this runner lives.
			return nil, nil, nil, fmt.Errorf("runner: load tool overrides: %w", err)
		}
		overrides = rows
	}
	for _, c := range nonCoreCandidates {
		name := c.tool.Definition().Name
		if !FilterToolEnabled(true, name, overrides) {
			continue
		}
		// Names were settled above, so this only fires if that pass and the
		// registry ever disagree.
		if err := toolReg.Register(c.tool); err != nil {
			return nil, nil, nil, fmt.Errorf("runner: register %s tool: %w", c.source, err)
		}
	}
	warnOrphanOverrides(overrides, sourceByName, knownBuiltinNames)

	return toolReg, hookSet, delegateTool, nil
}

// warnOrphanOverrides reports override rows that name no tool this deployment
// knows about. A stale row is a data problem, not a runner fault: a renamed or
// removed tool leaves its row behind, and failing the build over it would lock
// the user out of a working agent. Log it instead, so the row gets cleaned up
// before a future tool reuses the name and silently inherits the old setting.
// known holds every name this runner could have served (core plus every
// candidate, whether or not an override kept it out); knownBuiltins covers
// builtins this deployment ships but this run cannot use.
func warnOrphanOverrides(overrides []ToolOverride, known map[string]string, knownBuiltins map[string]struct{}) {
	for _, row := range overrides {
		if _, ok := known[row.ToolName]; ok {
			continue
		}
		if _, ok := knownBuiltins[row.ToolName]; ok {
			continue
		}
		if IsCoreToolName(row.ToolName) {
			continue
		}
		slog.Warn("tool override names a tool this runner does not know; ignoring",
			"component", "go_runner", "tool", row.ToolName, "scope", row.Scope, "enabled", row.Enabled)
	}
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
		if !reg.Has(name) {
			// A caller excluding a tool this runner never had is working from a
			// stale name list, not asking for something impossible. Hiding nothing
			// is the right outcome; say so instead of failing the run.
			slog.Warn("excluded tool is not registered for this runner; ignoring",
				"component", "go_runner", "tool", name)
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

func buildDelegatePresets(cfg runnerConfig, session pkgsandbox.Session) *delegatetool.PresetRegistry {
	env := session.Policy().Env
	roots := make([]delegatetool.PresetRoot, 0, 3)
	agentDelegatesSuffix := path.Join(".agents", "delegates")
	for _, mount := range session.Policy().Filesystem.Mounts {
		processPath := path.Clean(strings.ReplaceAll(mount.SandboxPath, "\\", "/"))
		if before, ok := strings.CutSuffix(processPath, "/"+agentDelegatesSuffix); ok {
			roots = append(roots, delegatetool.PresetRoot{
				Path:   before,
				Source: "agent",
			})
			break
		}
	}
	if len(roots) == 0 && cfg.BuiltinParams.UserID == "" && cfg.BuiltinParams.GroupID == "" && env[pkgsandbox.EnvHome] != "" {
		roots = append(roots, delegatetool.PresetRoot{Path: env[pkgsandbox.EnvHome], Source: "agent"})
	}
	if assets := env[pkgsandbox.EnvStellaAssetsDir]; assets != "" {
		roots = append(roots, delegatetool.PresetRoot{Path: path.Dir(strings.ReplaceAll(assets, "\\", "/")), Source: "user"})
	}
	roots = append(roots, delegatetool.PresetRoot{Path: session.WorkingDir(), Source: "project"})
	return delegatetool.NewPresetRegistry(delegatetool.LoadDelegatePresets(session.Files(), roots))
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
var ErrChatTimeout = agenterr.ErrChatTimeout

// sendEvent forwards evt to out unless ctx is done. It returns false when the
// consumer has gone away (ctx cancelled), so producers stop emitting instead of
// blocking forever on a buffered channel nobody is draining.
func sendEvent(ctx context.Context, out chan<- Event, evt Event) bool {
	select {
	case out <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}

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
					sendEvent(ctx, out, Event{Err: fmt.Errorf("runner: %w", err)})
					return
				}
				toolSet = filteredSet
				toolDefs = filteredDefs
			}
			tempRunner, err := newAgentRunnerWithTools(r.stream, r.model, r.streamOptions, effectiveSystem, r.hookSet, r.toolLifecycle, r.canonicalImages, toolSet, toolDefs, r.toolMode, r.codeToolSurface)
			if err != nil {
				sendEvent(ctx, out, Event{Err: fmt.Errorf("runner: %w", err)})
				return
			}
			loopRunner = tempRunner
		}

		// Inject session context into hook metadata so hooks can log it.
		loopRunner.SetHookMeta(hooks.HookMeta{
			SessionID: memory.SessionIDFromContext(ctx),
			UserID:    authz.UserIDFromContext(ctx),
			AgentID:   authz.AgentIDFromContext(ctx),
			Channel: func() string {
				channel, _ := ChannelFromContext(ctx)
				return channel
			}(),
		})

		// Nudge the model toward a summary as the wall-clock budget runs out.
		// The trigger is elapsed time, not a turn count. Turn milestones fire in
		// the middle of healthy work on fast turns, and the old turn-50 message
		// ("please report your current progress") was answered with a summary
		// and no tool call, which ends the loop: every unattended task was
		// capped at 50 turns regardless of how much of its budget was left.
		loopRunner.SetTurnNotify(progressNudge(timeout))

		// Reload the OAuth-derived session env before each turn so a long-lived
		// cached runner (kept warm by frequent scheduler fires) never hands tools
		// an expired OAuth token. A no-op on a fresh credential, on group
		// sessions, and on sessions without OAuth-sourced env (#722).
		if r.session != nil {
			sandbox.RefreshSessionEnv(ctx, r.session, r.sandboxCfg)
		}
		loopRunner.SetSecretValues(r.sandboxCfg.SessionSecretValues.Values())

		messages := make([]ai.Message, len(history))
		copy(messages, history)
		switch m := message.(type) {
		case ai.UserMessage:
			messages = append(messages, m)
		default:
			messages = append(messages, ai.UserMessage{Content: message, Timestamp: time.Now()})
		}

		if _, err := loopRunner.RunWithActiveStart(ctx, messages, len(history), func(e coreagent.LoopEvent) {
			for _, evt := range convertLoopEvent(e) {
				if !sendEvent(ctx, out, evt) {
					return
				}
			}
		}); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				err = fmt.Errorf("%w: %w", ErrChatTimeout, err)
			}
			sendEvent(ctx, out, Event{Err: err})
		}

		if r.session != nil {
			if err := sandbox.SyncSession(r.session); err != nil {
				slog.Warn("runner: sync session after chat", "error", err)
			}
		}
	}()

	return out
}

// Alive reports whether the runner is healthy. Capability-bearing runners
// delegate to the sandbox lifecycle; guest runners have no sandbox by design.
func (r *runner) Alive() bool {
	if r.session == nil {
		return r.noCapabilities
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

// RunManagedSession invokes the delegate instance configured for this runner.
// It is the one Session-tool bridge that retains the parent turn's preset,
// timeout, system-override, and tool-exclusion behavior.
func (r *runner) RunManagedSession(ctx context.Context, req delegatetool.ManagedSessionRequest) (delegatetool.ManagedSessionResult, error) {
	if r.delegateTool == nil {
		return delegatetool.ManagedSessionResult{}, fmt.Errorf("delegate tool is not configured")
	}
	return r.delegateTool.RunManagedSession(ctx, req)
}

// SandboxSession returns the live runner-owned sandbox for pre-close callers.
// Callers must not retain it after the runner is closed.
func (r *runner) SandboxSession() pkgsandbox.Session { return r.session }

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
	if r.cleanup != nil {
		if err := r.cleanup(); err != nil {
			errs = append(errs, err)
		}
		r.cleanup = nil
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// convertLoopEvent bridges agent.LoopEvent to Event(s).
func convertLoopEvent(e coreagent.LoopEvent) []Event {
	switch e := e.(type) {
	case coreagent.TurnStarted:
		return []Event{{Step: &StepEvent{Kind: "start"}}}

	case coreagent.TurnFinished:
		return []Event{{Step: &StepEvent{Kind: "finish"}}}

	case coreagent.AssistantDelta:
		switch d := e.Event.(type) {
		case ai.EventTextDelta:
			if d.Text != "" {
				return []Event{{Text: d.Text}}
			}
		case ai.EventThinkingDelta:
			if d.Thinking != "" {
				return []Event{{Reasoning: d.Thinking}}
			}
		}

	case coreagent.AssistantFinished:
		var events []Event
		for _, block := range e.Message.Content {
			if _, ok := block.(ai.ToolCall); ok {
				msg := ai.AssistantMessage{Content: e.Message.Content}
				events = append(events, Event{Store: msg})
				return events
			}
		}
		return events

	case coreagent.ToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			ID:        e.ToolCall.ID,
			Tool:      e.ToolCall.Name,
			Status:    "running",
			Input:     summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
			Arguments: e.ToolCall.Arguments,
		}}}

	case coreagent.ChildToolStarted:
		return []Event{{ToolUse: &ToolUseEvent{
			ID:        e.ToolCall.ID,
			Tool:      e.ToolCall.Name,
			Status:    "running",
			Input:     summarizeToolInput(e.ToolCall.Name, e.ToolCall.Arguments),
			Arguments: e.ToolCall.Arguments,
		}}}

	case coreagent.ChildToolFinished:
		status := "done"
		if e.Result.IsError {
			status = "error"
		}
		return []Event{{ToolUse: &ToolUseEvent{
			ID:      e.Result.ToolCallID,
			Tool:    e.Result.ToolName,
			Status:  status,
			Detail:  summarizeToolResult(e.Result),
			Content: ai.FlattenText(e.Result.Content),
		}}}

	case coreagent.ToolFinished:
		status := "done"
		if e.Result.IsError {
			status = "error"
		}
		stored := coreagent.NormalizeToolResult(e.Result)
		cleanText := ai.FlattenText(stored.Content)
		refs := stored.References
		// References live on the tool event as the single source of truth. The Web
		// SSE path reads them here; channel consumers (e.g. Feishu) read the event-
		// level field, which the coordinator fans out from ToolUse.References.
		return []Event{{
			ToolUse: &ToolUseEvent{
				ID:         e.Result.ToolCallID,
				Tool:       e.Result.ToolName,
				Status:     status,
				Detail:     summarizeToolResult(stored),
				Content:    cleanText,
				References: refs,
			},
			Store: stored,
		}}

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
	case "memory":
		action, _ := args["action"].(string)
		return action
	case "delegate":
		if tasks, ok := args["tasks"].([]any); ok && len(tasks) > 0 {
			return fmt.Sprintf("%d task(s)", len(tasks))
		}
	}
	return ""
}

// Progress-nudge thresholds, as a fraction of the chat budget. The first is a
// checkpoint the model must survive without stopping; the second is close
// enough to the deadline that wrapping up is the useful thing to do.
const (
	nudgeCheckpointFraction = 3.0 / 4.0
	nudgeWrapUpFraction     = 9.0 / 10.0
)

// progressNudge returns a turn-notify callback that fires at most twice per
// chat, on elapsed time rather than turn count. The wording matters as much as
// the trigger: a nudge the model reads as "stop and report" ends the loop,
// because a turn without a tool call is a finished turn.
func progressNudge(budget time.Duration) func(int, time.Duration) *string {
	checkpoint := time.Duration(float64(budget) * nudgeCheckpointFraction)
	wrapUp := time.Duration(float64(budget) * nudgeWrapUpFraction)
	var sentCheckpoint, sentWrapUp bool
	return func(_ int, elapsed time.Duration) *string {
		var msg string
		switch {
		case !sentWrapUp && elapsed >= wrapUp:
			sentWrapUp, sentCheckpoint = true, true
			msg = fmt.Sprintf("You have about %s left of a %s budget for this request. Finish or safely stop what you are doing now, then summarize what you completed and what remains.",
				(budget - elapsed).Round(time.Second), budget.Round(time.Second))
		case !sentCheckpoint && elapsed >= checkpoint:
			sentCheckpoint = true
			msg = fmt.Sprintf("Checkpoint: you have been working for %s of a %s budget. Briefly state your progress and then keep working. This is not a request to stop; if the approach is not converging, try a different one.",
				elapsed.Round(time.Second), budget.Round(time.Second))
		default:
			return nil
		}
		return &msg
	}
}
