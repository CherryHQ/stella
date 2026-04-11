package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// Notifier can push notifications. Both Dispatcher and individual channels
// satisfy this interface, so consumers don't need to know the routing layer.
type Notifier interface {
	Notify(ctx context.Context, n pkgchannel.Notification) error
}

type channelEntry struct {
	channel pkgchannel.Channel
}

type channelStore interface {
	ListChannels(ctx context.Context) ([]config.Channel, error)
}

// Dispatcher routes notifications to one or more registered channels.
// It implements Notifier so it can be passed to tools and scheduler wiring.
type Dispatcher struct {
	mu       sync.RWMutex
	channels []channelEntry
	auth     pkgplugins.Auth // optional; set via SetAuthService for per-user notifications
	store    channelStore    // optional; used to route agent-bound channel instances
}

// NewDispatcher creates an empty dispatcher. Register channels before use.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// Register adds a channel to the dispatcher.
func (d *Dispatcher) Register(ch pkgchannel.Channel) {
	d.mu.Lock()
	d.channels = append(d.channels, channelEntry{channel: ch})
	d.mu.Unlock()
}

// Unregister removes all channels with the given name from the dispatcher.
func (d *Dispatcher) Unregister(name string) {
	d.mu.Lock()
	filtered := d.channels[:0]
	for _, e := range d.channels {
		if !channelMatches(e.channel, name) {
			filtered = append(filtered, e)
		}
	}
	d.channels = filtered
	d.mu.Unlock()
}

// Notify routes a notification to channels. If Notification.Channel is set,
// only that channel receives it. Otherwise all registered channels receive it.
func (d *Dispatcher) Notify(ctx context.Context, n pkgchannel.Notification) error {
	d.mu.RLock()
	entries := make([]channelEntry, len(d.channels))
	copy(entries, d.channels)
	store := d.store
	d.mu.RUnlock()

	if len(entries) == 0 {
		return fmt.Errorf("no notification channels registered")
	}

	channels := listConfiguredChannels(ctx, store)

	if n.Channel != "" {
		if e, ok := exactEntry(entries, n.Channel); ok {
			return e.channel.Notify(ctx, n)
		}
		if e, ok := nonDedicatedEntryForPlatform(entries, channels, n.Channel); ok {
			return e.channel.Notify(ctx, n)
		}
		for _, e := range entries {
			if channelMatches(e.channel, n.Channel) {
				return e.channel.Notify(ctx, n)
			}
		}
		return fmt.Errorf("unknown notification channel %q", n.Channel)
	}

	if e, _, ok := dedicatedEntryForAgent(entries, channels, n.AgentID); ok {
		return e.channel.Notify(ctx, n)
	}

	entries = broadcastEntries(entries, channels)
	if len(entries) == 0 {
		return fmt.Errorf("no non-dedicated notification channels registered")
	}

	var errs []error
	for _, e := range entries {
		if err := e.channel.Notify(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.channel.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// SetAuthService configures the auth directory for per-user notification routing.
func (d *Dispatcher) SetAuthService(service pkgplugins.Auth) {
	d.mu.Lock()
	d.auth = service
	d.mu.Unlock()
}

// SetChannelStore configures the channel directory for agent-bound routing.
func (d *Dispatcher) SetChannelStore(store channelStore) {
	d.mu.Lock()
	d.store = store
	d.mu.Unlock()
}

// NotifyUser sends a notification to a specific user via a single channel.
//
// Resolution order:
//  1. If the notification came from an agent with a dedicated channel and the
//     user has that platform identity, use the dedicated channel.
//  2. If the user has a notify_identity_id preference, use that platform's
//     non-dedicated channel.
//  3. Otherwise use the first linked identity with a non-dedicated channel.
//
// Falls back to broadcast if the user has no linked identities or if no
// auth store is configured.
func (d *Dispatcher) NotifyUser(ctx context.Context, userID int64, n pkgchannel.Notification) error {
	d.mu.RLock()
	as := d.auth
	entries := make([]channelEntry, len(d.channels))
	copy(entries, d.channels)
	store := d.store
	d.mu.RUnlock()

	if len(entries) == 0 {
		return fmt.Errorf("no notification channels registered")
	}

	if as == nil {
		slog.Warn("notifyUser: no auth store configured, falling back to broadcast", "user_id", userID)
		return d.Notify(ctx, n)
	}

	identities, err := as.ListUserIdentities(ctx, userID)
	if err != nil {
		slog.Warn("notifyUser: failed to list identities, falling back to broadcast", "user_id", userID, "error", err)
		return d.Notify(ctx, n)
	}

	if len(identities) == 0 {
		slog.Debug("notifyUser: user has no linked identities, falling back to broadcast", "user_id", userID)
		return d.Notify(ctx, n)
	}

	target := pickNotifyIdentity(ctx, as, userID, identities)
	channels := listConfiguredChannels(ctx, store)

	if e, ch, ok := dedicatedEntryForAgent(entries, channels, n.AgentID); ok {
		if id, ok := identityForPlatform(identities, channelTypeForEntry(ch, e)); ok {
			nn := n
			nn.ChatID = id.ExternalID
			return e.channel.Notify(ctx, nn)
		}
	}

	if e, ok := nonDedicatedEntryForPlatform(entries, channels, target.Platform); ok {
		nn := n
		nn.ChatID = target.ExternalID
		return e.channel.Notify(ctx, nn)
	}

	for _, id := range identities {
		if e, ok := nonDedicatedEntryForPlatform(entries, channels, id.Platform); ok {
			slog.Warn("notifyUser: preferred channel not registered, using first available",
				"user_id", userID, "preferred", target.Platform, "fallback", id.Platform)
			nn := n
			nn.ChatID = id.ExternalID
			return e.channel.Notify(ctx, nn)
		}
	}

	slog.Debug("notifyUser: no matching channels for user identities, falling back to broadcast", "user_id", userID)
	return d.Notify(ctx, n)
}

type platformNamedChannel interface {
	Platform() string
}

func channelMatches(ch pkgchannel.Channel, name string) bool {
	if ch.Name() == name {
		return true
	}
	if typed, ok := ch.(platformNamedChannel); ok {
		return typed.Platform() == name
	}
	return false
}

func exactEntry(entries []channelEntry, name string) (channelEntry, bool) {
	for _, e := range entries {
		if e.channel.Name() == name {
			return e, true
		}
	}
	return channelEntry{}, false
}

func listConfiguredChannels(ctx context.Context, store channelStore) []config.Channel {
	if store == nil {
		return nil
	}
	channels, err := store.ListChannels(ctx)
	if err != nil {
		slog.Warn("notify: failed to list channel config, using registered channels only", "error", err)
		return nil
	}
	return channels
}

func channelTypeForEntry(ch config.Channel, entry channelEntry) string {
	if ch.Type != "" {
		return ch.Type
	}
	if typed, ok := entry.channel.(platformNamedChannel); ok {
		return typed.Platform()
	}
	return entry.channel.Name()
}

func configForEntry(channels []config.Channel, entry channelEntry) (config.Channel, bool) {
	for _, ch := range channels {
		if ch.ID == entry.channel.Name() {
			return ch, true
		}
	}
	return config.Channel{}, false
}

func entryIsDedicated(channels []config.Channel, entry channelEntry) bool {
	ch, ok := configForEntry(channels, entry)
	return ok && ch.AgentID != ""
}

func dedicatedEntryForAgent(entries []channelEntry, channels []config.Channel, agentID string) (channelEntry, config.Channel, bool) {
	if agentID == "" {
		return channelEntry{}, config.Channel{}, false
	}
	for _, ch := range channels {
		if !ch.Enabled || ch.AgentID != agentID {
			continue
		}
		for _, e := range entries {
			if e.channel.Name() == ch.ID {
				return e, ch, true
			}
		}
	}
	return channelEntry{}, config.Channel{}, false
}

func nonDedicatedEntryForPlatform(entries []channelEntry, channels []config.Channel, platform string) (channelEntry, bool) {
	if platform == "" {
		return channelEntry{}, false
	}
	if len(channels) == 0 {
		for _, e := range entries {
			if channelMatches(e.channel, platform) {
				return e, true
			}
		}
		return channelEntry{}, false
	}
	for _, e := range entries {
		ch, ok := configForEntry(channels, e)
		if ok {
			if ch.AgentID == "" && channelTypeForEntry(ch, e) == platform {
				return e, true
			}
			continue
		}
		if channelMatches(e.channel, platform) {
			return e, true
		}
	}
	return channelEntry{}, false
}

func broadcastEntries(entries []channelEntry, channels []config.Channel) []channelEntry {
	if len(channels) == 0 {
		return entries
	}
	out := make([]channelEntry, 0, len(entries))
	for _, e := range entries {
		if !entryIsDedicated(channels, e) {
			out = append(out, e)
		}
	}
	return out
}

func identityForPlatform(identities []pkgplugins.LinkedIdentity, platform string) (pkgplugins.LinkedIdentity, bool) {
	for _, id := range identities {
		if id.Platform == platform {
			return id, true
		}
	}
	return pkgplugins.LinkedIdentity{}, false
}

// pickNotifyIdentity returns the identity to use for notifications.
// If the user has a notify_identity_id preference that matches one of their
// linked identities, that identity is returned. Otherwise the first identity
// (earliest linked_at from the DB query) is used.
func pickNotifyIdentity(ctx context.Context, as pkgplugins.Auth, userID int64, identities []pkgplugins.LinkedIdentity) pkgplugins.LinkedIdentity {
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
