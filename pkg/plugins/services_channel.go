package plugins

import (
	"context"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// ChannelRegistry exposes channel registration needed by managed channel runtimes.
type ChannelRegistry interface {
	Register(pkgchannel.Channel)
	Unregister(name string)
}

// ChannelPlatform exposes the narrow platform services needed by managed channel runtimes.
type ChannelPlatform interface {
	ParentContext() context.Context
	Handler() pkgchannel.Handler
	Notifications() ChannelRegistry
}
