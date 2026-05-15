package plugins

import (
	"context"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// Notifier exposes user-visible notification delivery through the host.
type Notifier interface {
	Notify(ctx context.Context, n pkgchannel.Notification) error
	NotifyUser(ctx context.Context, userID string, n pkgchannel.Notification) error
}
