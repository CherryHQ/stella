package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type Option func(*Host)

type Host struct {
	store            config.Store
	log              *slog.Logger
	config           *configService
	runtimes         *RuntimeHost
	mu               sync.RWMutex
	pluginIDs        map[string]struct{}
	metadataRegs     map[string]pkgplugins.PluginInfo
	notifications    pkgplugins.Notifier
	scheduler        SchedulerBackend
	stateStore       StateStoreBackend
	authService      pkgplugins.Auth
	channelRuntime   pkgplugins.ChannelPlatform
	reflectRuntime   pkgplugins.ReflectPlatform
	toolRegs         map[string]pkgplugins.ToolSpec
	providerRegs     map[string]pkgplugins.ProviderSpec
	hookRegs         map[string]pkgplugins.HookSpec
	beforeRunRegs    map[string]pkgplugins.BeforeRunSpec
	beforeToolRegs   map[string]pkgplugins.BeforeToolCallSpec
	afterToolRegs    map[string]pkgplugins.AfterToolResultSpec
	channelRegs      map[string]pkgplugins.ChannelSpec
	memoryRegs       map[string]pkgplugins.MemorySpec
	runtimeRegs      map[string]pkgplugins.RuntimeSpec
	configRegs       map[string]pkgplugins.AdminSpec
	statusRegs       map[string]pkgplugins.AdminSpec
	promptRegs       map[string]pkgplugins.PromptInventorySpec
	systemPromptRegs map[string]pkgplugins.SystemPromptSpec
}

func New(store config.Store, opts ...Option) *Host {
	h := &Host{
		store:            store,
		log:              slog.With("component", "plugin_host"),
		pluginIDs:        map[string]struct{}{},
		metadataRegs:     map[string]pkgplugins.PluginInfo{},
		toolRegs:         map[string]pkgplugins.ToolSpec{},
		providerRegs:     map[string]pkgplugins.ProviderSpec{},
		hookRegs:         map[string]pkgplugins.HookSpec{},
		beforeRunRegs:    map[string]pkgplugins.BeforeRunSpec{},
		beforeToolRegs:   map[string]pkgplugins.BeforeToolCallSpec{},
		afterToolRegs:    map[string]pkgplugins.AfterToolResultSpec{},
		channelRegs:      map[string]pkgplugins.ChannelSpec{},
		memoryRegs:       map[string]pkgplugins.MemorySpec{},
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

func (h *Host) Logger(pluginID string) *slog.Logger { return h.log.With("plugin", pluginID) }
func (h *Host) Config() ConfigBackend               { return h.config }
func (h *Host) Runtime() pkgplugins.RuntimeLookup   { return h.runtimes }

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

func (h *Host) LoadCatalog(catalog *pkgplugins.Catalog) error {
	if catalog == nil {
		return nil
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

func (h *Host) AddProvider(reg pkgplugins.ProviderSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.providerRegs, reg.Name, reg, "provider")
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

func (h *Host) AddMemory(reg pkgplugins.MemorySpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.memoryRegs, reg.Name, reg, "memory")
}

func (h *Host) AddRuntime(reg pkgplugins.RuntimeSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.runtimeRegs, runtimeKey(reg.PluginID, reg.Name), reg, "runtime")
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

func (h *Host) AddBinary(_ pkgplugins.BinarySpec) {}

func registerUnique[T any](m map[string]T, key string, reg T, kind string) {
	if key == "" {
		panic("pluginhost: empty " + kind + " key")
	}
	if _, exists := m[key]; exists {
		panic(fmt.Sprintf("pluginhost: duplicate %s registration %q", kind, key))
	}
	m[key] = reg
}

func runtimeKey(pluginID, name string) string { return pluginID + "/" + name }
func promptKey(pluginID, name string) string  { return pluginID + "/" + name }

func (h *Host) SetEnabled(ctx context.Context, pluginID string, enabled bool) error {
	return h.config.SetEnabled(ctx, pluginID, enabled)
}

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

func (h *Host) ApplyChannel(ctx context.Context, channel config.Channel) error {
	return h.runtimes.ApplyChannel(ctx, channel)
}

func (h *Host) Stop(ctx context.Context) error { return h.runtimes.Stop(ctx) }

func (h *Host) PromptTools(ctx context.Context, pluginID string) ([]pkgplugins.PromptToolInfo, error) {
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
		state, err := h.DesiredState(ctx, reg.PluginID)
		if err != nil {
			state = pkgplugins.PluginState{ID: reg.PluginID, Config: h.defaultConfigFor(reg.PluginID)}
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
		state := build.State
		if reg.Required {
			state = pkgplugins.PluginState{
				ID:      reg.PluginID,
				Enabled: true,
				Config:  h.defaultConfigFor(reg.PluginID),
			}
		} else {
			var err error
			state, err = h.DesiredState(ctx, reg.PluginID)
			if err != nil || !state.Enabled {
				continue
			}
		}
		section, err := reg.Build(ctx, pkgplugins.SystemPromptContext{
			Platform:    h.platform(reg.PluginID),
			State:       state,
			AnnaHome:    build.AnnaHome,
			HomeDir:     build.HomeDir,
			AgentRoot:   build.AgentRoot,
			ProjectRoot: build.ProjectRoot,
			UserID:      build.UserID,
			AgentID:     build.AgentID,
			UserRoot:    build.UserRoot,
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

		state := build.State
		if reg.Required {
			state = pkgplugins.PluginState{
				ID:      reg.PluginID,
				Enabled: true,
				Config:  h.defaultConfigFor(reg.PluginID),
			}
		} else {
			var err error
			state, err = h.DesiredState(ctx, reg.PluginID)
			if err != nil || !state.Enabled {
				continue
			}
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
