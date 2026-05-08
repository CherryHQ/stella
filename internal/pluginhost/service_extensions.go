package pluginhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/skills"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

// WithNotificationService injects the narrow notification service available to plugins.
func WithNotificationService(service pkgplugins.Notifier) Option {
	return func(h *Host) {
		h.notifications = service
	}
}

// SetNotificationService updates the notification service extension after host construction.
func (h *Host) SetNotificationService(service pkgplugins.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifications = service
}

// Notifications returns the injected notification service, if any.
func (h *Host) Notifications() pkgplugins.Notifier {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.notifications
}

// WithSchedulerService injects the host scheduler backend available to plugins.
func WithSchedulerService(service SchedulerBackend) Option {
	return func(h *Host) {
		h.scheduler = service
	}
}

// SetSchedulerService updates the scheduler backend after host construction.
func (h *Host) SetSchedulerService(service SchedulerBackend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scheduler = service
}

// Scheduler returns the injected scheduler backend, if any.
func (h *Host) Scheduler() SchedulerBackend {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.scheduler
}

// WithStateStore injects the host plugin state backend available to plugins.
func WithStateStore(store StateStoreBackend) Option {
	return func(h *Host) {
		h.stateStore = store
	}
}

// SetStateStore updates the plugin state backend after host construction.
func (h *Host) SetStateStore(store StateStoreBackend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateStore = store
}

// StateStore returns the injected plugin state backend, if any.
func (h *Host) StateStore() StateStoreBackend {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stateStore
}

// WithAuthService injects the narrow auth directory available to plugins.
func WithAuthService(service pkgplugins.Auth) Option {
	return func(h *Host) {
		h.authService = service
	}
}

// SetAuthService updates the auth service extension after host construction.
func (h *Host) SetAuthService(service pkgplugins.Auth) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authService = service
}

// Auth returns the injected auth service, if any.
func (h *Host) Auth() pkgplugins.Auth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.authService
}

// WithChannelRuntimeServices injects the narrow services used by managed channel runtimes.
func WithChannelRuntimeServices(services pkgplugins.ChannelPlatform) Option {
	return func(h *Host) {
		h.channelRuntime = services
	}
}

// SetChannelRuntimeServices updates the channel runtime service extension after host construction.
func (h *Host) SetChannelRuntimeServices(services pkgplugins.ChannelPlatform) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.channelRuntime = services
}

// ChannelRuntime returns the injected channel runtime service extension, if any.
func (h *Host) ChannelRuntime() pkgplugins.ChannelPlatform {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.channelRuntime
}

// WithReflectRuntimeServices injects the narrow services used by the reflect managed runtime.
func WithReflectRuntimeServices(services pkgplugins.ReflectPlatform) Option {
	return func(h *Host) {
		h.reflectRuntime = services
	}
}

// SetReflectRuntimeServices updates the reflect runtime service extension after host construction.
func (h *Host) SetReflectRuntimeServices(services pkgplugins.ReflectPlatform) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reflectRuntime = services
}

// ReflectRuntime returns the injected reflect runtime service extension, if any.
func (h *Host) ReflectRuntime() pkgplugins.ReflectPlatform {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.reflectRuntime
}

// ChannelPlatform is a mutable host extension bag for managed channel runtimes.
type ChannelPlatform struct {
	mu            sync.RWMutex
	parent        context.Context
	handler       pkgchannel.Handler
	notifications pkgplugins.ChannelRegistry
}

func NewChannelRuntimeServices() *ChannelPlatform {
	return &ChannelPlatform{}
}

func (s *ChannelPlatform) Set(parent context.Context, handler pkgchannel.Handler, notifications pkgplugins.ChannelRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parent = parent
	s.handler = handler
	s.notifications = notifications
}

func (s *ChannelPlatform) ParentContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parent
}

func (s *ChannelPlatform) Handler() pkgchannel.Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handler
}

func (s *ChannelPlatform) Notifications() pkgplugins.ChannelRegistry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notifications
}

// ReflectPlatform is a mutable host extension bag for the reflect managed runtime.
type ReflectPlatform struct {
	mu             sync.RWMutex
	parent         context.Context
	memory         memory.Provider
	store          config.Store
	workspace      string
	buildProviders func(api, apiKey, baseURL string) (*providers.Registry, error)
}

func NewReflectRuntimeServices() *ReflectPlatform {
	return &ReflectPlatform{}
}

func (s *ReflectPlatform) Set(parent context.Context, mem memory.Provider, store config.Store, workspace string, buildProviders func(api, apiKey, baseURL string) (*providers.Registry, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parent = parent
	s.memory = mem
	s.store = store
	s.workspace = workspace
	s.buildProviders = buildProviders
}

func (s *ReflectPlatform) ParentContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.parent
}

func (s *ReflectPlatform) Memory() memory.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.memory
}

func (s *ReflectPlatform) Store() pkgplugins.ReflectStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil {
		return nil
	}
	return reflectConfigStore{store: s.store}
}

func (s *ReflectPlatform) Workspace() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspace
}

func (s *ReflectPlatform) BuildProviders(api, apiKey, baseURL string) (*providers.Registry, error) {
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

// WithSkillStore injects the skill store available to plugins.
func WithSkillStore(store skills.Store) Option {
	return func(h *Host) {
		h.skillStore = store
	}
}

// SetSkillStore updates the skill store after host construction.
func (h *Host) SetSkillStore(store skills.Store) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.skillStore = store
}

// SkillStore returns the injected skill store, if any.
func (h *Host) SkillStore() skills.Store {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.skillStore
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
