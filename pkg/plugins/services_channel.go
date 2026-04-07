package plugins

import (
	"context"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// NotificationRegistry exposes channel registration needed by managed channel runtimes.
type NotificationRegistry interface {
	Register(pkgchannel.Channel)
	Unregister(name string)
}

// ChannelRuntimeServices exposes the narrow platform services needed by managed channel runtimes.
type ChannelRuntimeServices interface {
	ParentContext() context.Context
	Handler() pkgchannel.Handler
	Notifications() NotificationRegistry
}
