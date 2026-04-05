package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaayne/anna/internal/auth"
)

// Notifier can push notifications. Both Dispatcher and individual channels
// satisfy this interface, so consumers don't need to know the routing layer.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

type channelEntry struct {
	channel Channel
}

// Dispatcher routes notifications to one or more registered channels.
// It implements Notifier so it can be passed to tools and scheduler wiring.
type Dispatcher struct {
	mu        sync.RWMutex
	channels  []channelEntry
	authStore auth.AuthStore // optional; set via SetAuthStore for per-user notifications
}

// NewDispatcher creates an empty dispatcher. Register channels before use.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// Register adds a channel to the dispatcher.
func (d *Dispatcher) Register(ch Channel) {
	d.mu.Lock()
	d.channels = append(d.channels, channelEntry{channel: ch})
	d.mu.Unlock()
}

// Notify routes a notification to channels. If Notification.Channel is set,
// only that channel receives it. Otherwise all registered channels receive it.
func (d *Dispatcher) Notify(ctx context.Context, n Notification) error {
	d.mu.RLock()
	entries := make([]channelEntry, len(d.channels))
	copy(entries, d.channels)
	d.mu.RUnlock()

	if len(entries) == 0 {
		return fmt.Errorf("no notification channels registered")
	}

	// Route to a specific channel.
	if n.Channel != "" {
		for _, e := range entries {
			if e.channel.Name() == n.Channel {
				return e.channel.Notify(ctx, n)
			}
		}
		return fmt.Errorf("unknown notification channel %q", n.Channel)
	}

	// Broadcast to all channels.
	var errs []error
	for _, e := range entries {
		if err := e.channel.Notify(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.channel.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// SetAuthStore configures the auth store for per-user notification routing.
func (d *Dispatcher) SetAuthStore(store auth.AuthStore) {
	d.mu.Lock()
	d.authStore = store
	d.mu.Unlock()
}

// NotifyUser sends a notification to a specific user via a single channel.
//
// Resolution order:
//  1. If the user has a notify_identity_id preference, use that identity.
//  2. Otherwise use the first linked identity (earliest linked_at).
//
// Falls back to broadcast if the user has no linked identities or if no
// auth store is configured.
func (d *Dispatcher) NotifyUser(ctx context.Context, userID int64, n Notification) error {
	d.mu.RLock()
	as := d.authStore
	entries := make([]channelEntry, len(d.channels))
	copy(entries, d.channels)
	d.mu.RUnlock()

	if len(entries) == 0 {
		return fmt.Errorf("no notification channels registered")
	}

	// If no auth store, fall back to broadcast.
	if as == nil {
		slog.Warn("notifyUser: no auth store configured, falling back to broadcast", "user_id", userID)
		return d.Notify(ctx, n)
	}

	identities, err := as.ListIdentitiesByUser(ctx, userID)
	if err != nil {
		slog.Warn("notifyUser: failed to list identities, falling back to broadcast", "user_id", userID, "error", err)
		return d.Notify(ctx, n)
	}

	// No linked identities — fall back to broadcast.
	if len(identities) == 0 {
		slog.Debug("notifyUser: user has no linked identities, falling back to broadcast", "user_id", userID)
		return d.Notify(ctx, n)
	}

	// Pick the target identity: preferred > first linked.
	target := pickNotifyIdentity(ctx, as, userID, identities)

	// Find the matching registered channel and send.
	for _, e := range entries {
		if e.channel.Name() == target.Platform {
			nn := n
			nn.ChatID = target.ExternalID
			return e.channel.Notify(ctx, nn)
		}
	}

	// Preferred platform has no registered channel — use first linked
	// identity that has a registered channel.
	for _, id := range identities {
		for _, e := range entries {
			if e.channel.Name() == id.Platform {
				slog.Warn("notifyUser: preferred channel not registered, using first available",
					"user_id", userID, "preferred", target.Platform, "fallback", id.Platform)
				nn := n
				nn.ChatID = id.ExternalID
				return e.channel.Notify(ctx, nn)
			}
		}
	}

	slog.Debug("notifyUser: no matching channels for user identities, falling back to broadcast", "user_id", userID)
	return d.Notify(ctx, n)
}

// pickNotifyIdentity returns the identity to use for notifications.
// If the user has a notify_identity_id preference that matches one of their
// linked identities, that identity is returned. Otherwise the first identity
// (earliest linked_at from the DB query) is used.
func pickNotifyIdentity(ctx context.Context, as auth.AuthStore, userID int64, identities []auth.Identity) auth.Identity {
	user, err := as.GetUser(ctx, userID)
	if err != nil {
		slog.Warn("notifyUser: failed to get user, using first identity", "user_id", userID, "error", err)
		return identities[0]
	}

	if user.NotifyIdentityID != nil {
		for _, id := range identities {
			if id.ID == *user.NotifyIdentityID {
				return id
			}
		}
		slog.Warn("notifyUser: preferred identity not found in linked identities, using first",
			"user_id", userID, "notify_identity_id", *user.NotifyIdentityID)
	}

	return identities[0]
}

// Channels returns the names of all registered channels.
func (d *Dispatcher) Channels() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, len(d.channels))
	for i, e := range d.channels {
		names[i] = e.channel.Name()
	}
	return names
}
