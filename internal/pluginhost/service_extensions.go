package pluginhost

import (
	"context"
	"sync"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

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
