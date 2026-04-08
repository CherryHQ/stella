package pluginhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
)

// WithNotificationService injects the narrow notification service available to plugins.
func WithNotificationService(service pkgplugins.NotificationService) Option {
	return func(h *Host) {
		h.notifications = service
	}
}

// SetNotificationService updates the notification service extension after host construction.
func (h *Host) SetNotificationService(service pkgplugins.NotificationService) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifications = service
}

// Notifications returns the injected notification service, if any.
func (h *Host) Notifications() pkgplugins.NotificationService {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.notifications
}

// WithStateStore injects the narrow plugin state store available to plugins.
func WithStateStore(store pkgplugins.PluginStateStore) Option {
	return func(h *Host) {
		h.stateStore = store
	}
}

// SetStateStore updates the plugin state store extension after host construction.
func (h *Host) SetStateStore(store pkgplugins.PluginStateStore) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateStore = store
}

// StateStore returns the injected plugin state store, if any.
func (h *Host) StateStore() pkgplugins.PluginStateStore {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stateStore
}

// WithChannelRuntimeServices injects the narrow services used by managed channel runtimes.
func WithChannelRuntimeServices(services pkgplugins.ChannelRuntimeServices) Option {
	return func(h *Host) {
		h.channelRuntime = services
	}
}

// SetChannelRuntimeServices updates the channel runtime service extension after host construction.
func (h *Host) SetChannelRuntimeServices(services pkgplugins.ChannelRuntimeServices) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.channelRuntime = services
}

// ChannelRuntime returns the injected channel runtime service extension, if any.
func (h *Host) ChannelRuntime() pkgplugins.ChannelRuntimeServices {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.channelRuntime
}

// WithReflectRuntimeServices injects the narrow services used by the reflect managed runtime.
func WithReflectRuntimeServices(services pkgplugins.ReflectRuntimeServices) Option {
	return func(h *Host) {
		h.reflectRuntime = services
	}
}

// SetReflectRuntimeServices updates the reflect runtime service extension after host construction.
func (h *Host) SetReflectRuntimeServices(services pkgplugins.ReflectRuntimeServices) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reflectRuntime = services
}

// ReflectRuntime returns the injected reflect runtime service extension, if any.
func (h *Host) ReflectRuntime() pkgplugins.ReflectRuntimeServices {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.reflectRuntime
}

// ChannelRuntimeServices is a mutable host extension bag for managed channel runtimes.
type ChannelRuntimeServices struct {
	mu            sync.RWMutex
	parent        context.Context
	handler       pkgchannel.Handler
	notifications pkgplugins.NotificationRegistry
}

func NewChannelRuntimeServices() *ChannelRuntimeServices {
	return &ChannelRuntimeServices{}
}

func (s *ChannelRuntimeServices) Set(parent context.Context, handler pkgchannel.Handler, notifications pkgplugins.NotificationRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parent = parent
	s.handler = handler
	s.notifications = notifications
}

func (s *ChannelRuntimeServices) ParentContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parent
}

func (s *ChannelRuntimeServices) Handler() pkgchannel.Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handler
}

func (s *ChannelRuntimeServices) Notifications() pkgplugins.NotificationRegistry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notifications
}

// ReflectRuntimeServices is a mutable host extension bag for the reflect managed runtime.
type ReflectRuntimeServices struct {
	mu             sync.RWMutex
	parent         context.Context
	memory         memory.Provider
	store          config.Store
	workspace      string
	buildProviders func(api, apiKey, baseURL string) (*providers.Registry, error)
}

func NewReflectRuntimeServices() *ReflectRuntimeServices {
	return &ReflectRuntimeServices{}
}

func (s *ReflectRuntimeServices) Set(parent context.Context, mem memory.Provider, store config.Store, workspace string, buildProviders func(api, apiKey, baseURL string) (*providers.Registry, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parent = parent
	s.memory = mem
	s.store = store
	s.workspace = workspace
	s.buildProviders = buildProviders
}

func (s *ReflectRuntimeServices) ParentContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parent
}

func (s *ReflectRuntimeServices) Memory() memory.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.memory
}

func (s *ReflectRuntimeServices) Store() pkgplugins.ReflectStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil {
		return nil
	}
	return reflectConfigStore{store: s.store}
}

func (s *ReflectRuntimeServices) Workspace() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspace
}

func (s *ReflectRuntimeServices) BuildProviders(api, apiKey, baseURL string) (*providers.Registry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.buildProviders == nil {
		return nil, fmt.Errorf("reflect: provider builder unavailable")
	}
	return s.buildProviders(api, apiKey, baseURL)
}

type reflectConfigStore struct {
	store config.Store
}

func (s reflectConfigStore) ListEnabledAgents(ctx context.Context) ([]pkgplugins.ReflectAgent, error) {
	agents, err := s.store.ListEnabledAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.ReflectAgent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, pkgplugins.ReflectAgent{ID: agent.ID})
	}
	return out, nil
}

func (s reflectConfigStore) Snapshot(ctx context.Context, agentID string) (*pkgplugins.ReflectSnapshot, error) {
	snap, err := s.store.Snapshot(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := &pkgplugins.ReflectSnapshot{
		AgentID:      snap.AgentID,
		Provider:     snap.Provider,
		Model:        snap.Model,
		ModelStrong:  snap.ModelStrong,
		ModelFast:    snap.ModelFast,
		Workspace:    snap.Workspace,
		APIKey:       snap.APIKey,
		BaseURL:      snap.BaseURL,
		SystemPrompt: snap.SystemPrompt,
		Providers:    map[string]pkgplugins.ReflectProviderCreds{},
	}
	for providerID, creds := range snap.Providers {
		out.Providers[providerID] = pkgplugins.ReflectProviderCreds{
			APIKey:  creds.APIKey,
			BaseURL: creds.BaseURL,
		}
	}
	return out, nil
}
