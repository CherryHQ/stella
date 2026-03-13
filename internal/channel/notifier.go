package channel

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
// It implements Notifier so it can be passed to tools and cron wiring.
type Dispatcher struct {
	mu       sync.RWMutex
	channels []channelEntry
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
