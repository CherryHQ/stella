package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaayne/anna/internal/auth"
)

// Notification represents a message to push to a user or channel.
type Notification struct {
	Channel string // optional: route to a specific backend ("telegram", "slack")
	ChatID  string // target chat/channel within the backend
	Text    string // markdown content
	Silent  bool   // send without notification sound
}

// Notifier can push notifications. Both Dispatcher and individual channels
// satisfy this interface, so consumers don't need to know the routing layer.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

type channelEntry struct {
	channel     Channel
	defaultChat string
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

// Register adds a channel with its default chat/channel target.
func (d *Dispatcher) Register(ch Channel, defaultChat string) {
	d.mu.Lock()
	d.channels = append(d.channels, channelEntry{channel: ch, defaultChat: defaultChat})
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
				if n.ChatID == "" {
					n.ChatID = e.defaultChat
				}
				return e.channel.Notify(ctx, n)
			}
		}
		return fmt.Errorf("unknown notification channel %q", n.Channel)
	}

	// Broadcast to all channels.
	var errs []error
	for _, e := range entries {
		nn := n
		if nn.ChatID == "" {
			nn.ChatID = e.defaultChat
		}
		if err := e.channel.Notify(ctx, nn); err != nil {
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

// NotifyUser sends a notification to a specific user by resolving their
// channel identities. Falls back to broadcast if the user has no linked
// identities or if no auth store is configured.
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

	// Build a map of platform -> external_id for quick lookup.
	platformIDs := make(map[string]string, len(identities))
	for _, id := range identities {
		platformIDs[id.Platform] = id.ExternalID
	}

	// Send to each channel where the user has a linked identity.
	var errs []error
	sent := false
	for _, e := range entries {
		externalID, ok := platformIDs[e.channel.Name()]
		if !ok {
			continue
		}
		nn := n
		nn.ChatID = externalID
		if err := e.channel.Notify(ctx, nn); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.channel.Name(), err))
		} else {
			sent = true
		}
	}

	// If nothing was sent (no matching channels), fall back to broadcast.
	if !sent && len(errs) == 0 {
		slog.Debug("notifyUser: no matching channels for user identities, falling back to broadcast", "user_id", userID)
		return d.Notify(ctx, n)
	}

	return errors.Join(errs...)
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
