package admin

import (
	"context"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// SetChannelLifecycle configures the admin server with the helpers needed to
// build channel instances and wire notification registration.
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
