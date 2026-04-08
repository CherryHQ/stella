package plugins

import (
	"context"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// NotificationService exposes user-visible notification delivery through the host.
type NotificationService interface {
	Notify(ctx context.Context, n pkgchannel.Notification) error
	NotifyUser(ctx context.Context, userID int64, n pkgchannel.Notification) error
}
