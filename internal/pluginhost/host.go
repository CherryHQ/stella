package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type Option func(*Host)

type Host struct {
	store        config.Store
	log          *slog.Logger
	config       *configService
	runtimes     *RuntimeHost
	mu           sync.RWMutex
	pluginIDs    map[string]struct{}
	legacyIDs    map[string]string
	toolRegs     map[string]pkgplugins.ToolRegistration
	providerRegs map[string]pkgplugins.ProviderRegistration
	hookRegs     map[string]pkgplugins.HookRegistration
	channelRegs  map[string]pkgplugins.ChannelRegistration
	memoryRegs   map[string]pkgplugins.MemoryRegistration
	runtimeRegs  map[string]pkgplugins.RuntimeRegistration
	configRegs   map[string]pkgplugins.ConfigRegistration
	statusRegs   map[string]pkgplugins.StatusRegistration
	promptRegs   map[string]pkgplugins.PromptInventoryRegistration
}

func New(store config.Store, opts ...Option) *Host {
	h := &Host{
		store:        store,
		log:          slog.With("component", "plugin_host"),
		pluginIDs:    map[string]struct{}{},
		legacyIDs:    map[string]string{},
		toolRegs:     map[string]pkgplugins.ToolRegistration{},
		providerRegs: map[string]pkgplugins.ProviderRegistration{},
		hookRegs:     map[string]pkgplugins.HookRegistration{},
		channelRegs:  map[string]pkgplugins.ChannelRegistration{},
		memoryRegs:   map[string]pkgplugins.MemoryRegistration{},
		runtimeRegs:  map[string]pkgplugins.RuntimeRegistration{},
		configRegs:   map[string]pkgplugins.ConfigRegistration{},
		statusRegs:   map[string]pkgplugins.StatusRegistration{},
		promptRegs:   map[string]pkgplugins.PromptInventoryRegistration{},
	}
	h.config = &configService{store: store, aliases: map[string]string{}}
	h.runtimes = NewRuntimeHost(h)
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Host) Registry() pkgplugins.RegistryHost { return h }
func (h *Host) Services() pkgplugins.ServiceHost  { return h }

func (h *Host) Logger(pluginID string) *slog.Logger { return h.log.With("plugin", pluginID) }
func (h *Host) Config() pkgplugins.ConfigService    { return h.config }
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

func (h *Host) RegisterLegacyID(pluginID, legacyID string) {
	if pluginID == "" || legacyID == "" {
		panic("pluginhost: empty legacy id mapping")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.legacyIDs[legacyID]; ok && existing != pluginID {
		panic(fmt.Sprintf("pluginhost: legacy id %q already mapped to %q", legacyID, existing))
	}
	h.legacyIDs[legacyID] = pluginID
	h.config.aliases[pluginID] = legacyID
}

func (h *Host) resolvePluginID(id string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if _, ok := h.pluginIDs[id]; ok {
		return id
	}
	if canonical, ok := h.legacyIDs[id]; ok {
		return canonical
	}
	return id
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
	return nil
}

func (h *Host) LoadDefaultCatalog() error { return h.LoadCatalog(defaultCatalog()) }

func (h *Host) RegisterTool(reg pkgplugins.ToolRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.toolRegs, reg.Name, reg, "tool")
}
func (h *Host) RegisterProvider(reg pkgplugins.ProviderRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.providerRegs, reg.Name, reg, "provider")
}
func (h *Host) RegisterChannel(reg pkgplugins.ChannelRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.channelRegs, reg.Name, reg, "channel")
}
func (h *Host) RegisterHook(reg pkgplugins.HookRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.hookRegs, reg.Name, reg, "hook")
}
func (h *Host) RegisterMemory(reg pkgplugins.MemoryRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.memoryRegs, reg.Name, reg, "memory")
}
func (h *Host) RegisterRuntime(reg pkgplugins.RuntimeRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.runtimeRegs, runtimeKey(reg.PluginID, reg.Name), reg, "runtime")
}
func (h *Host) RegisterConfig(reg pkgplugins.ConfigRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.configRegs, reg.PluginID, reg, "config")
}
func (h *Host) RegisterStatus(reg pkgplugins.StatusRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.statusRegs, reg.PluginID, reg, "status")
}
func (h *Host) RegisterPromptInventory(reg pkgplugins.PromptInventoryRegistration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	registerUnique(h.promptRegs, promptKey(reg.PluginID, reg.Name), reg, "prompt inventory")
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

func runtimeKey(pluginID, name string) string { return pluginID + "/" + name }
func promptKey(pluginID, name string) string  { return pluginID + "/" + name }

func (h *Host) ResolvePluginID(id string) string { return h.resolvePluginID(id) }

func (h *Host) SetEnabled(ctx context.Context, pluginID string, enabled bool) error {
	return h.config.SetEnabled(ctx, h.resolvePluginID(pluginID), enabled)
}

func (h *Host) Status(ctx context.Context, pluginID string) (any, error) {
	canonical := h.resolvePluginID(pluginID)
	h.mu.RLock()
	reg, ok := h.statusRegs[canonical]
	h.mu.RUnlock()
	if !ok || reg.Get == nil {
		return map[string]any{}, nil
	}
	return reg.Get(ctx)
}

func (h *Host) ValidateConfig(pluginID string, raw map[string]any) error {
	canonical := h.resolvePluginID(pluginID)
	h.mu.RLock()
	reg, ok := h.configRegs[canonical]
	h.mu.RUnlock()
	if !ok || reg.Validate == nil {
		return nil
	}
	return reg.Validate(raw)
}

func (h *Host) RedactConfig(pluginID string, raw map[string]any) map[string]any {
	canonical := h.resolvePluginID(pluginID)
	h.mu.RLock()
	reg, ok := h.configRegs[canonical]
	h.mu.RUnlock()
	if !ok {
		return cloneMap(raw)
	}
	return reg.Redacted(raw)
}

func (h *Host) DesiredState(ctx context.Context, pluginID string) (pkgplugins.PluginState, error) {
	return h.config.Get(ctx, h.resolvePluginID(pluginID))
}

func (h *Host) ApplyPlugin(ctx context.Context, pluginID string) error {
	return h.runtimes.ApplyPlugin(ctx, h.resolvePluginID(pluginID))
}

func (h *Host) Stop(ctx context.Context) error { return h.runtimes.Stop(ctx) }

func (h *Host) PromptTools(ctx context.Context, pluginID string) ([]pkgplugins.PromptToolInfo, error) {
	canonical := h.resolvePluginID(pluginID)
	h.mu.RLock()
	regs := make([]pkgplugins.PromptInventoryRegistration, 0, len(h.promptRegs))
	for _, reg := range h.promptRegs {
		if reg.PluginID == canonical {
			regs = append(regs, reg)
		}
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	var out []pkgplugins.PromptToolInfo
	for _, reg := range regs {
		if reg.GetTools == nil {
			continue
		}
		tools, err := reg.GetTools(ctx)
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			out = append(out, tool.Clone())
		}
	}
	return out, nil
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
