package plugins

import (
	"context"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ChannelRegistry exposes channel registration needed by managed channel runtimes.
type ChannelRegistry interface {
	Register(pkgchannel.Channel)
	Unregister(name string)
}

// HandlerWrapper is an optional composition-root policy applied to a managed
// channel's handler for the lifetime of one runtime operation.
type HandlerWrapper func(pkgchannel.Handler, context.Context) pkgchannel.Handler

// ChannelPlatform exposes the narrow platform services needed by managed channel runtimes.
type ChannelPlatform interface {
	ParentContext() context.Context
	Handler() pkgchannel.Handler
	Notifications() ChannelRegistry
	WrapHandler() HandlerWrapper
	BuildVersion() string
	Enrollment() pkgchannel.AccountEnroller
}
