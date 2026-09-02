package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
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

type resolvedChannel struct {
	entry channelEntry
	cfg   config.Channel
}

type routingTable struct {
	byName             map[string]channelEntry
	firstByType        map[string]channelEntry
	nonDedicatedByType map[string]channelEntry
	dedicatedByAgent   map[string]resolvedChannel
	broadcast          []channelEntry
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

// Notify routes a notification to channels.
//
// Resolution order:
//  1. If the agent has a dedicated channel that matches the requested channel
//     type (or no channel type is specified), use the dedicated channel.
//  2. If Notification.Channel is set, route to that specific channel.
//  3. Otherwise broadcast to all non-dedicated channels.
func (d *Dispatcher) Notify(ctx context.Context, n pkgchannel.Notification) error {
	table, err := d.routingTable(ctx)
	if err != nil {
		return err
	}

	if n.AgentID != "" {
		if dedicated, ok := table.dedicatedByAgent[n.AgentID]; ok {
			dType := resolvedChannelType(dedicated)
			dName := dedicated.entry.channel.Name()
			if n.Channel == "" || n.Channel == dName || n.Channel == dType {
				slog.Debug("notify: routing to agent-dedicated channel",
					"agent_id", n.AgentID, "channel", dName, "type", dType)
				return dedicated.entry.channel.Notify(ctx, n)
			}
			slog.Debug("notify: agent has dedicated channel but type mismatch",
				"agent_id", n.AgentID, "dedicated_type", dType, "requested", n.Channel)
		}
	}

	if n.Channel != "" {
		if entry, ok := table.entryForNotificationChannel(n.Channel); ok {
			slog.Debug("notify: routing to explicit channel",
				"channel", n.Channel, "resolved", entry.channel.Name())
			return entry.channel.Notify(ctx, n)
		}
		return fmt.Errorf("unknown notification channel %q", n.Channel)
	}

	slog.Debug("notify: broadcasting", "agent_id", n.AgentID, "targets", len(table.broadcast))
	return notifyEntries(ctx, table.broadcast, n, "no non-dedicated notification channels registered")
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
func (d *Dispatcher) NotifyUser(ctx context.Context, userID string, n pkgchannel.Notification) error {
	table, authService, err := d.routingTableAndAuth(ctx)
	if err != nil {
		return err
	}

	if authService == nil {
		slog.Warn("notifyUser: no auth store configured, falling back to broadcast", "user_id", userID)
		return notifyEntries(ctx, table.broadcast, n, "no non-dedicated notification channels registered")
	}

	identities, err := authService.ListUserIdentities(ctx, userID)
	if err != nil {
		slog.Warn("notifyUser: failed to list identities, falling back to broadcast", "user_id", userID, "error", err)
		return notifyEntries(ctx, table.broadcast, n, "no non-dedicated notification channels registered")
	}
	if len(identities) == 0 {
		slog.Debug("notifyUser: user has no linked identities, falling back to broadcast", "user_id", userID)
		return notifyEntries(ctx, table.broadcast, n, "no non-dedicated notification channels registered")
	}

	target := pickNotifyIdentity(ctx, authService, userID, identities)
	if dedicated, ok := table.dedicatedByAgent[n.AgentID]; ok {
		if id, ok := identityForPlatform(identities, resolvedChannelType(dedicated)); ok {
			return notifyWithChatID(ctx, dedicated.entry, n, id.ExternalID)
		}
	}

	if entry, ok := table.nonDedicatedByType[target.Platform]; ok {
		return notifyWithChatID(ctx, entry, n, target.ExternalID)
	}

	for _, id := range identities {
		if entry, ok := table.nonDedicatedByType[id.Platform]; ok {
			slog.Warn("notifyUser: preferred channel not registered, using first available",
				"user_id", userID, "preferred", target.Platform, "fallback", id.Platform)
			return notifyWithChatID(ctx, entry, n, id.ExternalID)
		}
	}

	slog.Debug("notifyUser: no matching channels for user identities, falling back to broadcast", "user_id", userID)
	return notifyEntries(ctx, table.broadcast, n, "no non-dedicated notification channels registered")
}

type platformNamedChannel interface {
	Platform() string
}

func channelMatches(ch pkgchannel.Channel, name string) bool {
	return ch.Name() == name
}

func (d *Dispatcher) routingTable(ctx context.Context) (routingTable, error) {
	d.mu.RLock()
	entries := append([]channelEntry(nil), d.channels...)
	store := d.store
	d.mu.RUnlock()

	if len(entries) == 0 {
		return routingTable{}, fmt.Errorf("no notification channels registered")
	}

	return buildRoutingTable(entries, listConfiguredChannels(ctx, store)), nil
}

func (d *Dispatcher) routingTableAndAuth(ctx context.Context) (routingTable, pkgplugins.Auth, error) {
	d.mu.RLock()
	authService := d.auth
	d.mu.RUnlock()
	table, err := d.routingTable(ctx)
	return table, authService, err
}

func buildRoutingTable(entries []channelEntry, channels []config.Channel) routingTable {
	table := routingTable{
		byName:             make(map[string]channelEntry, len(entries)),
		firstByType:        make(map[string]channelEntry, len(entries)),
		nonDedicatedByType: make(map[string]channelEntry, len(entries)),
		dedicatedByAgent:   make(map[string]resolvedChannel),
		broadcast:          make([]channelEntry, 0, len(entries)),
	}

	configured := make(map[string]config.Channel, len(channels))
	for _, ch := range channels {
		configured[ch.ID] = ch
	}

	for _, entry := range entries {
		name := entry.channel.Name()
		table.byName[name] = entry

		cfg, hasConfig := configured[name]
		channelType := channelTypeForEntry(cfg, entry, hasConfig)
		if _, ok := table.firstByType[channelType]; !ok && channelType != "" {
			table.firstByType[channelType] = entry
		}

		if hasConfig {
			if !cfg.Enabled {
				continue
			}
			if cfg.AgentID != "" {
				table.dedicatedByAgent[cfg.AgentID] = resolvedChannel{entry: entry, cfg: cfg}
				continue
			}
			if _, ok := table.nonDedicatedByType[channelType]; !ok {
				table.nonDedicatedByType[channelType] = entry
			}
			table.broadcast = append(table.broadcast, entry)
			continue
		}

		if _, ok := table.nonDedicatedByType[channelType]; !ok && channelType != "" {
			table.nonDedicatedByType[channelType] = entry
		}
		table.broadcast = append(table.broadcast, entry)
	}

	return table
}

func (t routingTable) entryForNotificationChannel(channel string) (channelEntry, bool) {
	if channel == "" {
		return channelEntry{}, false
	}
	if entry, ok := t.byName[channel]; ok {
		return entry, true
	}
	if entry, ok := t.nonDedicatedByType[channel]; ok {
		return entry, true
	}
	entry, ok := t.firstByType[channel]
	return entry, ok
}

func notifyEntries(ctx context.Context, entries []channelEntry, n pkgchannel.Notification, emptyMessage string) error {
	if len(entries) == 0 {
		return errors.New(emptyMessage)
	}

	var errs []error
	for _, entry := range entries {
		if err := entry.channel.Notify(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.channel.Name(), err))
		}
	}
	return errors.Join(errs...)
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

func channelTypeForEntry(ch config.Channel, entry channelEntry, hasConfig bool) string {
	if hasConfig && ch.Type != "" {
		return ch.Type
	}
	if typed, ok := entry.channel.(platformNamedChannel); ok {
		if platform := typed.Platform(); platform != "" {
			return platform
		}
	}
	if hasConfig && ch.ID != "" {
		return ch.ID
	}
	return entry.channel.Name()
}

func resolvedChannelType(ch resolvedChannel) string {
	if typed, ok := ch.entry.channel.(platformNamedChannel); ok {
		if platform := typed.Platform(); platform != "" {
			return platform
		}
	}
	return channelTypeForEntry(ch.cfg, ch.entry, true)
}

func identityForPlatform(identities []pkgplugins.LinkedIdentity, platform string) (pkgplugins.LinkedIdentity, bool) {
	for _, id := range identities {
		if id.Platform == platform {
			return id, true
		}
	}
	return pkgplugins.LinkedIdentity{}, false
}

func notifyWithChatID(ctx context.Context, entry channelEntry, n pkgchannel.Notification, chatID string) error {
	nn := n
	nn.ChatID = chatID
	nn.RecipientID = chatID
	return entry.channel.Notify(ctx, nn)
}

// pickNotifyIdentity returns the identity to use for notifications.
// If the user has a notify_identity_id preference that matches one of their
// linked identities, that identity is returned. Otherwise the first identity
// (earliest linked_at from the DB query) is used.
func pickNotifyIdentity(ctx context.Context, as pkgplugins.Auth, userID string, identities []pkgplugins.LinkedIdentity) pkgplugins.LinkedIdentity {
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
