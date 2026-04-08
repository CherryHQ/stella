package admin

import (
	"context"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// RegisterChannelStop registers a stop function for a running channel so the
// admin panel can stop it when it is disabled via the UI.
func (s *Server) RegisterChannelStop(platform string, stop func()) {
	s.channelMu.Lock()
	s.channelStop[platform] = stop
	s.channelMu.Unlock()
}

// SetChannelLifecycle configures the admin server to start/stop channels dynamically.
// builder creates a channel by platform name; notify registers it for notifications;
// unnotify removes it from the notification dispatcher on stop.
func (s *Server) SetChannelLifecycle(
	ctx context.Context,
	builder func(name string) (pkgchannel.Channel, error),
	notify func(name string, ch pkgchannel.Channel),
	unnotify func(name string),
) {
	s.channelMu.Lock()
	s.channelBuilder = builder
	s.channelNotify = notify
	s.channelUnnotify = unnotify
	s.channelCtx = ctx
	s.channelMu.Unlock()
}

// startChannel builds, starts, and registers a channel for the given platform.
// No-op if the channel is already running or no builder is configured.
func (s *Server) startChannel(platform string) {
	s.channelMu.RLock()
	_, running := s.channelStop[platform]
	builder := s.channelBuilder
	notify := s.channelNotify
	ctx := s.channelCtx
	s.channelMu.RUnlock()

	if running || builder == nil {
		return
	}

	ch, err := builder(platform)
	if err != nil {
		s.log.Error("failed to build channel", "platform", platform, "error", err)
		return
	}

	s.RegisterChannelStop(platform, ch.Stop)
	if notify != nil {
		notify(platform, ch)
	}

	s.log.Info("starting channel", "platform", platform)
	go func() {
		if err := ch.Start(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("channel stopped with error", "platform", platform, "error", err)
		}
	}()
}

// stopChannel stops a running channel if one is registered for the platform.
func (s *Server) stopChannel(platform string) {
	s.channelMu.RLock()
	stop, ok := s.channelStop[platform]
	unnotify := s.channelUnnotify
	s.channelMu.RUnlock()
	if ok {
		s.log.Info("stopping channel", "platform", platform)
		stop()
		if unnotify != nil {
			unnotify(platform)
		}
		s.channelMu.Lock()
		delete(s.channelStop, platform)
		s.channelMu.Unlock()
	}
}
