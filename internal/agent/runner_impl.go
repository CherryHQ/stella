package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agenterr"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/renderrefs"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
	"github.com/CherryHQ/stella/resources"
)

// providerConfig groups LLM provider settings.
type providerConfig struct {
	ProviderID string   // canonical provider row ID used in qualified model refs
	API        string   // provider adapter type: "anthropic", "openai"
	Model      string   // e.g. "claude-sonnet-4-20250514"
	Input      []string // declared model input modalities, e.g. ["text", "image"]; nil when undeclared
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
	SkillStore           pkgplugins.SkillStore
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
	Cleanup              func() error
}

// runner implements Runner by calling LLM providers directly via agent.Runner.
type runner struct {
	runner          *coreagent.Runner
	stream          providers.StreamFunc
	tools           *tools.Registry
	delegateTool    *delegatetool.DelegateTool
	model           ai.Model
	streamOptions   ai.StreamOptions
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
	model := ai.Model{ID: cfg.Provider.Model, API: cfg.Provider.API, Name: cfg.Provider.Model, Provider: providerID, BaseURL: cfg.Provider.BaseURL, Input: cfg.Provider.Input}

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
		paths, err := sandbox.ResolvePaths(cfg.Sandbox)
		if err != nil {
			if session != nil {
				_ = session.Close()
			}
			return nil, fmt.Errorf("runner: %w", err)
		}
		systemPrompt = prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
			StellaHome:  paths.StellaHome,
			AgentRoot:   paths.AgentRoot,
			ProjectRoot: paths.ProjectRoot,
			UserRoot:    paths.UserRoot,
			Sections:    cfg.Sections,
			Host:        session,
		})
	}

	toolReg, hookSet, delegateTool, err := buildToolRegistry(ctx, cfg, session, stream, model, systemPrompt)
	if err != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, err
	}

	streamOptions := ai.StreamOptions{Reasoning: cfg.Thinking}
	coreRunner, err := newAgentRunner(stream, toolReg, model, streamOptions, systemPrompt, hookSet, cfg.ToolLifecycle, cfg.CanonicalImages)
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

func newAgentRunner(stream providers.StreamFunc, toolReg *tools.Registry, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle, canonicalImages *coreagent.CanonicalImageConfig) (*coreagent.Runner, error) {
	toolSet := coreagent.ToolSetFromRegistry(toolReg)
	toolDefs := toolReg.Definitions()
	return newAgentRunnerWithTools(stream, model, streamOptions, system, hookSet, toolLifecycle, canonicalImages, toolSet, toolDefs)
}

func newAgentRunnerWithTools(stream providers.StreamFunc, model ai.Model, streamOptions ai.StreamOptions, system string, hookSet *hooks.HookSet, toolLifecycle *coreagent.ToolLifecycle, canonicalImages *coreagent.CanonicalImageConfig, toolSet coreagent.ToolSet, toolDefs []tools.Definition) (*coreagent.Runner, error) {
	opts := []coreagent.Option{
		coreagent.WithStreamOptions(streamOptions),
		coreagent.WithSystem(system),
		coreagent.WithHooks(hookSet, hooks.HookMeta{}),
		coreagent.WithToolLifecycle(toolLifecycle),
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

// buildToolRegistry creates the tool registry with core, builtin, and external tools.
func buildToolRegistry(ctx context.Context, cfg runnerConfig, session pkgsandbox.Session, stream providers.StreamFunc, model ai.Model, systemPrompt string) (*tools.Registry, *hooks.HookSet, *delegatetool.DelegateTool, error) {
	toolReg := tools.NewRegistry()
	if cfg.NoCapabilities {
		return toolReg, nil, nil, nil
	}
	paths, _ := sandbox.ResolvePaths(cfg.Sandbox)

	// Core tools (read, bash, edit, write) are always provided by the active
	// sandbox session.

	toolsBinDir := resolveToolsBinDir(paths, config.SandboxBackendDocker)

	// Runtime capabilities are injected from the active runner session.
	bc := pkgplugins.ToolBuildContext{
		Paths: pkgplugins.ToolPaths{
			UserRoot:      paths.UserRoot,
			WorkspaceRoot: paths.WorkspaceRoot,
			ToolsBinDir:   toolsBinDir,
			StellaHome:    paths.StellaHome,
			AgentRoot:     paths.AgentRoot,
			ProjectRoot:   paths.ProjectRoot,
		},
		Runtime: session,
	}

	coreTools := buildSandboxCoreTools(session, bc, cfg.Sandbox.SessionSecretValues)
	if len(coreTools) == 0 {
		return nil, nil, nil, fmt.Errorf("runner: sandbox backend unavailable: core tools require an active sandbox host")
	}

	// Sandbox core tools (bash/read/write/edit) route through the active
	// session and must win over any plugin tool of the same name. Plugin
	// versions run in the stella process, which would bypass the sandbox.
	coreNames := make(map[string]struct{}, len(coreTools))
	for _, t := range coreTools {
		coreNames[t.Definition().Name] = struct{}{}
		toolReg.Register(t)
	}

	var nonCoreCandidates []tools.Tool
	registerNonCore := func(t tools.Tool) {
		name := t.Definition().Name
		if _, taken := coreNames[name]; taken {
			slog.Debug("skipping non-sandbox tool that collides with sandbox core",
				"component", "go_runner", "tool", name)
			return
		}
		nonCoreCandidates = append(nonCoreCandidates, t)
	}

	for _, entry := range cfg.BuiltinTools {
		if entry.Tool == nil {
			return nil, nil, nil, fmt.Errorf("runner: builtin tool is nil")
		}
		if entry.Available != nil && !entry.Available(ctx, cfg.BuiltinParams) {
			continue
		}
		registerNonCore(entry.Tool)
	}
	for _, t := range cfg.PerRunTools {
		registerNonCore(t)
	}
	if cfg.SkillStore != nil {
		stellaHome := paths.StellaHome
		toolProjectRoot := paths.ProjectRoot
		layout, view := skillRuntimeLayoutAndView(ctx, cfg, paths)
		registerNonCore(skillstool.NewTool(cfg.SkillStore, stellaHome, toolProjectRoot).
			WithProjectSnapshot(cfg.ProjectSkillSnapshot).
			WithSkillDiskLayout(layout).
			WithSkillDirView(view).
			WithPluginVisibility(cfg.PluginView.RegisteredPluginIDs, cfg.PluginView.EnabledPluginIDs).
			WithAgentSkillPolicy(cfg.DisabledSkillRefs).
			WithReadAuthorizer(cfg.SkillReadAuthorizer).
			WithActionsOnly("search_installed", "load"))
	}
	if cfg.MCPToolProvider != nil {
		for _, t := range cfg.MCPToolProvider.ToolsForContext(ctx, cfg.BuiltinParams.UserID, cfg.BuiltinParams.AgentID) {
			registerNonCore(t)
		}
	}
	if cfg.PluginTools != nil {
		for _, t := range cfg.PluginTools(ctx, bc) {
			registerNonCore(t)
		}
	}

	hookSet := buildHookSet(cfg)
	delegateTool := delegatetool.NewDelegateTool(delegatetool.DelegateConfig{
		Stream:         stream,
		Registry:       toolReg,
		Model:          model,
		System:         systemPrompt,
		Presets:        buildDelegatePresets(cfg),
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
			slog.Warn("failed to load tool overrides; using default tool visibility", "error", err)
		} else {
			overrides = rows
		}
	}
	for _, t := range nonCoreCandidates {
		name := t.Definition().Name
		if !FilterToolEnabled(true, name, overrides) {
			continue
		}
		toolReg.Register(t)
	}

	return toolReg, hookSet, delegateTool, nil
}

// skillRuntimeLayoutAndView is the sole production boundary that decides
// which filesystem Skill tiers a runner can expose to the model.
func skillRuntimeLayoutAndView(ctx context.Context, cfg runnerConfig, paths sandbox.Paths) (skillstool.SkillDiskLayout, skillstool.SkillDirView) {
	userDataDir, workspaceRoot := paths.UserDataDir, paths.WorkspaceRoot
	if cfg.BuiltinParams.GroupID != "" {
		userDataDir, workspaceRoot = "", ""
	}
	layout := skillDiskLayout(SystemDBSkillsDir(paths.StellaHome), paths.AgentRoot, userDataDir, workspaceRoot)
	sv := sandbox.ResolveSkillView(ctx, cfg.Sandbox, paths)
	view := skillstool.SkillDirView{
		Isolated: sv.Isolated, BuiltinSkillsHost: sv.BuiltinSkillsHost, BuiltinSkillsView: sv.BuiltinSkillsView,
		AgentSkillsHost: sv.AgentSkillsHost, AgentSkillsView: sv.AgentSkillsView,
		SystemDBSkillsHost: sv.SystemDBSkillsHost, SystemDBSkillsView: sv.SystemDBSkillsView,
		UserDataHost: sv.UserDataHost, UserDataView: sv.UserDataView,
		WorkspaceHost: sv.WorkspaceHost, WorkspaceView: sv.WorkspaceView,
	}
	if cfg.BuiltinParams.GroupID != "" {
		view.UserDataHost, view.UserDataView = "", ""
		view.WorkspaceHost, view.WorkspaceView = "", ""
	}
	return layout, view
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

func buildDelegatePresets(cfg runnerConfig) *delegatetool.PresetRegistry {
	paths, _ := sandbox.ResolvePaths(cfg.Sandbox)
	if err := resources.ExtractDelegates(stellaDelegatesDir(paths)); err != nil {
		slog.Warn("failed to extract builtin delegates", "error", err)
	}
	return delegatetool.NewPresetRegistry(delegatetool.LoadDelegatePresets(delegatetool.LoadDelegatePresetsConfig{
		StellaHome: paths.StellaHome,
		AgentRoot:  paths.AgentRoot,
		// User-level presets live under the shared user-data root (mounted as /user).
		UserRoot:    paths.UserDataDir,
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
			tempRunner, err := newAgentRunnerWithTools(r.stream, r.model, r.streamOptions, effectiveSystem, r.hookSet, r.toolLifecycle, r.canonicalImages, toolSet, toolDefs)
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

		// Reload the OAuth-derived session env before each turn so a long-lived
		// cached runner (kept warm by frequent scheduler fires) never hands tools
		// an expired OAuth token. A no-op on a fresh credential, on group
		// sessions, and on sessions without OAuth-sourced env (#722).
		if r.session != nil {
			sandbox.RefreshSessionEnv(ctx, r.session, r.sandboxCfg)
		}

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

	case coreagent.ToolFinished:
		status := "done"
		if e.Result.IsError {
			status = "error"
		}
		// Tool results carry a single text block today, so the first one is the
		// whole body. If a producer ever emits multiple text blocks, a sentinel in
		// a later block is missed here — LCM's per-block scrub at ingest is the
		// backstop that still keeps it out of the persisted/replayed text.
		var fullText string
		for _, block := range e.Result.Content {
			if tc, ok := block.(ai.TextContent); ok {
				fullText = tc.Text
				break
			}
		}
		cleanText, refs := renderrefs.Extract(fullText)
		// Persist the stripped text whenever Extract removed anything — a real ref
		// or a malformed/truncated sentinel — so the saved conversation (and any
		// later replay into the model) never carries a raw sentinel. Summarize the
		// detail from the same cleaned result for the same reason.
		stored := e.Result
		if cleanText != fullText {
			stored = cleanToolResult(e.Result, cleanText)
		}
		stored.References = refs
		// References live on the tool event as the single source of truth. The Web
		// SSE path reads them here; channel consumers (e.g. Feishu) read the event-
		// level field, which the coordinator fans out from ToolUse.References.
		return []Event{
			{ToolUse: &ToolUseEvent{
				ID:         e.Result.ToolCallID,
				Tool:       e.Result.ToolName,
				Status:     status,
				Detail:     summarizeToolResult(stored),
				Content:    cleanText,
				References: refs,
			}},
			{Store: stored},
		}

	case coreagent.AgentErrored:
		return []Event{{Err: e.Err}}
	}

	return nil
}

// cleanToolResult returns a copy of result with its first text block replaced by
// clean, so the persisted tool result carries no renderref sentinel. Other
// blocks (images, etc.) are shared unchanged.
func cleanToolResult(result ai.ToolResultMessage, clean string) ai.ToolResultMessage {
	out := result
	out.Content = make([]ai.ContentBlock, len(result.Content))
	copy(out.Content, result.Content)
	for i, block := range out.Content {
		if tc, ok := block.(ai.TextContent); ok {
			tc.Text = clean
			out.Content[i] = tc
			break
		}
	}
	return out
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
