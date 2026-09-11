package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/CherryHQ/stella/internal/plugin"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type Option func(*Host)

// ListenerCap decides whether a channel instance may accept new ingress.
// pluginID identifies the platform plugin and agentID identifies the bound
// channel owner. The gate is required for channel runtime admission.
type ListenerCap func(context.Context, string, string) (bool, error)

// ErrListenerCapUnavailable means the common listener policy was not wired.
// Channel runtimes fail closed when this gate is absent.
var ErrListenerCapUnavailable = errors.New("pluginhost: listener capability unavailable")

type Host struct {
	store    config.Store
	log      *slog.Logger
	config   *configService
	runtimes *RuntimeHost
	mu       sync.RWMutex
	// sealed is set by Seal() after all static registrations and capability
	// bindings are complete. Once sealed, the static composition surface
	// (LoadCatalog and the Set* capability binders) refuses further changes,
	// while the dynamic desired-state surface (ApplyPlugin/ReconcileChannel/
	// Stop) stays available.
	sealed           bool
	pluginIDs        map[string]struct{}
	metadataRegs     map[string]pkgplugins.PluginInfo
	notifications    pkgplugins.Notifier
	stateStore       StateStoreBackend
	authService      pkgplugins.Auth
	enrollment       AccountEnrollmentBackend
	nativePolicy     *plugin.NativePolicy
	channelRuntime   pkgplugins.ChannelPlatform
	listenerCap      ListenerCap
	toolRegs         map[string]pkgplugins.ToolSpec
	hookRegs         map[string]pkgplugins.HookSpec
	beforeRunRegs    map[string]pkgplugins.BeforeRunSpec
	beforeToolRegs   map[string]pkgplugins.BeforeToolCallSpec
	afterToolRegs    map[string]pkgplugins.AfterToolResultSpec
	channelRegs      map[string]pkgplugins.ChannelSpec
	runtimeRegs      map[string]pkgplugins.RuntimeSpec
	configRegs       map[string]pkgplugins.AdminSpec
	statusRegs       map[string]pkgplugins.AdminSpec
	promptRegs       map[string]pkgplugins.PromptInventorySpec
	systemPromptRegs map[string]pkgplugins.SystemPromptSpec

	// channelLeases, when bound via WithChannelLeases, gates durable channel
	// start on the cross-replica runtime lease.
	channelLeases *ChannelLeases
}

func New(store config.Store, opts ...Option) *Host {
	h := &Host{
		store:            store,
		log:              slog.With("component", "plugin_host"),
		pluginIDs:        map[string]struct{}{},
		metadataRegs:     map[string]pkgplugins.PluginInfo{},
		toolRegs:         map[string]pkgplugins.ToolSpec{},
		hookRegs:         map[string]pkgplugins.HookSpec{},
		beforeRunRegs:    map[string]pkgplugins.BeforeRunSpec{},
		beforeToolRegs:   map[string]pkgplugins.BeforeToolCallSpec{},
		afterToolRegs:    map[string]pkgplugins.AfterToolResultSpec{},
		channelRegs:      map[string]pkgplugins.ChannelSpec{},
		runtimeRegs:      map[string]pkgplugins.RuntimeSpec{},
		configRegs:       map[string]pkgplugins.AdminSpec{},
		statusRegs:       map[string]pkgplugins.AdminSpec{},
		promptRegs:       map[string]pkgplugins.PromptInventorySpec{},
		systemPromptRegs: map[string]pkgplugins.SystemPromptSpec{},
	}
	h.config = &configService{store: store}
	h.runtimes = NewRuntimeHost(h)
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ChannelLeases returns the bound lease tracker, or nil when the legacy
// unconditional-start mode is in effect.
func (h *Host) ChannelLeases() *ChannelLeases {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.channelLeases
}

func (h *Host) Logger(pluginID string) *slog.Logger { return h.log.With("plugin", pluginID) }
func (h *Host) Config() ConfigBackend               { return h.config }
func (h *Host) Runtime() pkgplugins.RuntimeLookup   { return h.runtimes }

// SetNativePolicy binds the trusted native admission policy before the host is
// sealed. Every capability registered through this Go host uses this policy;
// Agent package resources are composed separately by the runner.
func (h *Host) SetNativePolicy(policy *plugin.NativePolicy) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requireUnsealedLocked("SetNativePolicy")
	h.nativePolicy = policy
}

func (h *Host) nativeState(ctx context.Context, pluginID, agentID string) (pkgplugins.PluginState, bool, error) {
	h.mu.RLock()
	policy := h.nativePolicy
	h.mu.RUnlock()
	if policy == nil {
		return pkgplugins.PluginState{}, false, plugin.ErrNativePolicyUnavailable
	}
	allowed, err := policy.Allows(ctx, pluginID, agentID)
	if err != nil {
		return pkgplugins.PluginState{}, false, err
	}
	if !allowed {
		return pkgplugins.PluginState{}, false, nil
	}
	global, err := policy.GlobalPlugin(ctx, pluginID)
	if err != nil {
		return pkgplugins.PluginState{}, false, err
	}
	if !global.Enabled {
		return pkgplugins.PluginState{}, false, nil
	}
	var stateConfig map[string]any
	if global.Config != nil {
		stateConfig = cloneMap(global.Config)
	}
	return pkgplugins.PluginState{ID: pluginID, Enabled: true, Config: stateConfig}, true, nil
}

// ListChannelChats returns the group chats currently joined by a channel bot.
func (h *Host) ListChannelChats(ctx context.Context, channelID string, pageSize int, pageToken string) (pkgchannel.JoinedChatPage, error) {
	return h.runtimes.ListChannelChats(ctx, channelID, pageSize, pageToken)
}

func (h *Host) RegisterPluginID(id string) {
	if id == "" {
		panic("pluginhost: empty plugin id")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.pluginIDs[id]; exists {
		panic(fmt.Sprintf("pluginhost: duplicate plugin id %q", id))
	}
	h.pluginIDs[id] = struct{}{}
}

// Seal validates every static registration and capability binding, then marks
// the host sealed so no further static composition can occur. Missing capability
// registrations fail here; duplicate static registrations already fail eagerly
// at registration time (registerUnique). After Seal, LoadCatalog and the Set*
// capability binders refuse late changes, while the dynamic desired-state
// surface (ApplyPlugin/ReconcileChannel/Stop)
// remains available. Seal is one-shot.
func (h *Host) Seal() error {
	if err := h.ValidateRegistrations(); err != nil {
		return err
	}
	if err := h.validateCapabilityBackings(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sealed {
		return errors.New("pluginhost: already sealed")
	}
	h.sealed = true
	return nil
}

// requireUnsealedLocked panics if the host is sealed. Callers must already hold
// h.mu. A late static registration is a composition bug, mirroring the eager
// panic registerUnique raises for duplicates.
func (h *Host) requireUnsealedLocked(op string) {
	if h.sealed {
		panic("pluginhost: " + op + " after Seal (static registration is sealed)")
	}
}

func (h *Host) LoadCatalog(catalog *pkgplugins.Catalog) error {
	if catalog == nil {
		return nil
	}
	h.mu.RLock()
	sealed := h.sealed
	h.mu.RUnlock()
	if sealed {
		return errors.New("pluginhost: LoadCatalog after Seal")
	}
	for _, id := range catalog.Names() {
		plugin, ok := catalog.Get(id)
		if !ok {
			continue
		}
		h.RegisterPluginID(id)
		plugin.Register(h)
	}
	if err := h.ValidateRegistrations(); err != nil {
		return err
	}
	return nil
}

func (h *Host) LoadDefaultCatalog() error { return h.LoadCatalog(defaultCatalog()) }

func (h *Host) SetInfo(info pkgplugins.PluginInfo) {
	info = normalizeMetadata(info)
	validateMetadataShape(info)

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pluginIDs[info.ID]; !ok {
		panic(fmt.Sprintf("pluginhost: metadata registered for unknown plugin id %q", info.ID))
	}
	registerUnique(h.metadataRegs, info.ID, info, "metadata")
}

func (h *Host) AddAdmin(reg pkgplugins.AdminSpec) {
	h.mu.Lock()
	if reg.DefaultConfig != nil || len(reg.Schema) > 0 || reg.Validate != nil || reg.Redact != nil {
		registerUnique(h.configRegs, reg.PluginID, reg, "config")
	}
	if reg.Status != nil {
		registerUnique(h.statusRegs, reg.PluginID, reg, "status")
	}
	h.mu.Unlock()
}

func (h *Host) AddTool(reg pkgplugins.ToolSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.toolRegs, reg.Name, reg, "tool")
}

func (h *Host) AddChannel(reg pkgplugins.ChannelSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.channelRegs, reg.Name, reg, "channel")
}

func (h *Host) AddHook(reg pkgplugins.HookSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.hookRegs, reg.Name, reg, "hook")
}

func (h *Host) AddBeforeRun(reg pkgplugins.BeforeRunSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.beforeRunRegs, promptKey(reg.PluginID, reg.Name), reg, "before run")
}

func (h *Host) AddBeforeToolCall(reg pkgplugins.BeforeToolCallSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.beforeToolRegs, promptKey(reg.PluginID, reg.Name), reg, "before tool call")
}

func (h *Host) AddAfterToolResult(reg pkgplugins.AfterToolResultSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.afterToolRegs, promptKey(reg.PluginID, reg.Name), reg, "after tool result")
}

func (h *Host) AddRuntime(reg pkgplugins.RuntimeSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.runtimeRegs, runtimeRegKey(reg.PluginID, reg.Name), reg, "runtime")
}

func (h *Host) AddPromptInventory(reg pkgplugins.PromptInventorySpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.promptRegs, promptKey(reg.PluginID, reg.Name), reg, "prompt inventory")
}

func (h *Host) AddSystemPrompt(reg pkgplugins.SystemPromptSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.systemPromptRegs, promptKey(reg.PluginID, reg.Name), reg, "system prompt")
}

func registerUnique[T any](m map[string]T, key string, reg T, kind string) {
	if key == "" {
		panic("pluginhost: empty " + kind + " key")
	}
	if _, exists := m[key]; exists {
		panic(fmt.Sprintf("pluginhost: duplicate %s registration %q", kind, key))
	}
	m[key] = reg
}

func runtimeRegKey(pluginID, name string) string { return pluginID + "/" + name }
func promptKey(pluginID, name string) string     { return pluginID + "/" + name }

func (h *Host) Status(ctx context.Context, pluginID string) (any, error) {
	h.mu.RLock()
	reg, ok := h.statusRegs[pluginID]
	h.mu.RUnlock()
	if !ok || reg.Status == nil {
		return map[string]any{}, nil
	}
	state, err := h.DesiredState(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	return reg.Status(ctx, pkgplugins.AdminContext{Platform: h.platform(pluginID), State: state})
}

func (h *Host) ValidateConfig(pluginID string, raw map[string]any) error {
	h.mu.RLock()
	reg, ok := h.configRegs[pluginID]
	h.mu.RUnlock()
	if !ok || reg.Validate == nil {
		return nil
	}
	return reg.Validate(raw)
}

// IsConfigurable reports whether the plugin has an admin config schema
// registered. Used by the admin API to reject writes to plugin IDs that
// no longer exist in code (orphan plugin rows from old installs)
// or to typo'd IDs, instead of silently accepting them.
func (h *Host) IsConfigurable(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.configRegs[pluginID]
	return ok
}

func (h *Host) RedactConfig(pluginID string, raw map[string]any) map[string]any {
	h.mu.RLock()
	reg, ok := h.configRegs[pluginID]
	h.mu.RUnlock()
	if !ok {
		return cloneMap(raw)
	}
	return reg.Redacted(raw)
}

func (h *Host) ConfigSchema(pluginID string) map[string]any {
	h.mu.RLock()
	reg, ok := h.configRegs[pluginID]
	h.mu.RUnlock()
	if !ok {
		return map[string]any{}
	}
	return reg.SchemaDefinition()
}

func (h *Host) DesiredState(ctx context.Context, pluginID string) (pkgplugins.PluginState, error) {
	return h.config.Get(ctx, pluginID)
}

func (h *Host) ApplyPlugin(ctx context.Context, pluginID string) error {
	return h.runtimes.ApplyPlugin(ctx, pluginID)
}

// ReconcileChannel reapplies one committed channel instance by exact ID.
func (h *Host) ReconcileChannel(ctx context.Context, channelID string) error {
	return h.runtimes.ReconcileChannel(ctx, channelID)
}

func (h *Host) listenerAllowed(ctx context.Context, pluginID, agentID string) (bool, error) {
	h.mu.RLock()
	cap := h.listenerCap
	h.mu.RUnlock()
	if cap == nil {
		return false, ErrListenerCapUnavailable
	}
	allowed, err := cap(ctx, pluginID, agentID)
	if err != nil {
		return false, fmt.Errorf("listener capability for %s/%s: %w", pluginID, agentID, err)
	}
	return allowed, nil
}

// Quiesce halts new ingress on managed runtimes (channel pollers) for a graceful
// drain while preserving already-accepted operations and notifier senders. The
// runtime table is left intact so a later Stop can fully tear them down.
func (h *Host) Quiesce(ctx context.Context) { h.runtimes.Quiesce(ctx) }

func (h *Host) Stop(ctx context.Context) error { return h.runtimes.Stop(ctx) }

func (h *Host) PromptTools(ctx context.Context, pluginID, agentID string) ([]pkgplugins.PromptToolInfo, error) {
	h.mu.RLock()
	regs := make([]pkgplugins.PromptInventorySpec, 0, len(h.promptRegs))
	for _, reg := range h.promptRegs {
		if reg.PluginID == pluginID {
			regs = append(regs, reg)
		}
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool {
		return promptKey(regs[i].PluginID, regs[i].Name) < promptKey(regs[j].PluginID, regs[j].Name)
	})
	var out []pkgplugins.PromptToolInfo
	for _, reg := range regs {
		if reg.GetTools == nil {
			continue
		}
		state, enabled, err := h.nativeState(ctx, reg.PluginID, agentID)
		if err != nil {
			return nil, err
		}
		if !enabled {
			continue
		}
		tools, err := reg.GetTools(ctx, pkgplugins.PromptInventoryContext{Platform: h.platform(reg.PluginID), State: state})
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			out = append(out, tool.Clone())
		}
	}
	return out, nil
}

func (h *Host) SystemPromptSections(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
	h.mu.RLock()
	regs := make([]pkgplugins.SystemPromptSpec, 0, len(h.systemPromptRegs))
	for _, reg := range h.systemPromptRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool {
		return promptKey(regs[i].PluginID, regs[i].Name) < promptKey(regs[j].PluginID, regs[j].Name)
	})
	var out []pkgplugins.SystemPromptSection
	for _, reg := range regs {
		if reg.Build == nil {
			continue
		}
		state, enabled, err := h.nativeState(ctx, reg.PluginID, build.AgentID)
		if err != nil {
			return nil, err
		}
		if !enabled {
			continue
		}
		section, err := reg.Build(ctx, pkgplugins.SystemPromptContext{
			Platform:            h.platform(reg.PluginID),
			State:               state,
			UserID:              build.UserID,
			AgentID:             build.AgentID,
			RegisteredPluginIDs: append([]string(nil), build.RegisteredPluginIDs...),
			EnabledPluginIDs:    append([]string(nil), build.EnabledPluginIDs...),
		})
		if err != nil {
			return nil, err
		}
		if section.Title == "" || section.Content == "" {
			continue
		}
		out = append(out, section)
	}
	return out, nil
}

func (h *Host) BeforeRun(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
	h.mu.RLock()
	regs := make([]pkgplugins.BeforeRunSpec, 0, len(h.beforeRunRegs))
	for _, reg := range h.beforeRunRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool {
		if regs[i].Order != regs[j].Order {
			return regs[i].Order < regs[j].Order
		}
		if regs[i].PluginID != regs[j].PluginID {
			return regs[i].PluginID < regs[j].PluginID
		}
		return regs[i].Name < regs[j].Name
	})

	current := build.SystemPrompt
	for _, reg := range regs {
		if reg.Run == nil {
			continue
		}

		state, enabled, err := h.nativeState(ctx, reg.PluginID, build.AgentID)
		if err != nil {
			return pkgplugins.BeforeRunResult{}, err
		}
		if !enabled {
			continue
		}

		result, err := reg.Run(ctx, pkgplugins.BeforeRunContext{
			Platform:     h.platform(reg.PluginID),
			State:        state,
			SessionID:    build.SessionID,
			Channel:      build.Channel,
			UserID:       build.UserID,
			AgentID:      build.AgentID,
			Model:        build.Model,
			MessageText:  build.MessageText,
			SystemPrompt: current,
			History:      append([]ai.Message(nil), build.History...),
		})
		if err != nil {
			return pkgplugins.BeforeRunResult{}, err
		}
		if result.SystemPrompt != "" {
			current = result.SystemPrompt
		}
	}

	return pkgplugins.BeforeRunResult{SystemPrompt: current}, nil
}

func cloneMap(src map[string]any) map[string]any {
	return pkgplugins.PluginState{Config: src}.Clone().Config
}
