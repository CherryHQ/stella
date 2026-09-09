package discord

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

const discordCursorStream = "gateway"

// gatewayCursor records gateway sequence numbers after the corresponding
// inbound callback has been handled. discordgo stores its live sequence in a
// private field and updates it before dispatching handlers, so SyncEvents alone
// cannot make the platform cursor durable. This queue makes the ordering
// explicit at our boundary and persists only contiguous completed events.
type gatewayCursor struct {
	store     channel.IngressCursorStore
	channelID string
	streamKey string

	mu      sync.Mutex
	flushMu sync.Mutex
	loaded  bool
	cursor  int64
	queue   []*gatewayCursorEntry
	ctx     context.Context
}

type gatewayCursorEntry struct {
	sequence     int64
	typeName     string
	handled      bool
	started      bool
	needsHandler bool
	skip         bool
}

func newGatewayCursor(store channel.IngressCursorStore, channelID string) *gatewayCursor {
	return &gatewayCursor{store: store, channelID: channelID, streamKey: discordCursorStream}
}

func (c *gatewayCursor) enabled() bool {
	return c != nil && c.store != nil
}

func (c *gatewayCursor) current() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursor
}

func (c *gatewayCursor) setContext(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
}

func (c *gatewayCursor) load(ctx context.Context) error {
	if !c.enabled() {
		return errors.New("discord durable ingress cursor store is not configured")
	}
	cursor, err := c.store.LoadIngressCursor(ctx, channel.PlatformDiscord, c.channelID, c.streamKey)
	if err != nil {
		return fmt.Errorf("load Discord ingress cursor: %w", err)
	}
	c.mu.Lock()
	c.cursor = cursor
	c.loaded = true
	c.mu.Unlock()
	return nil
}

// observe runs from discordgo's public *Event callback. discordgo has already
// atomically stored Event.Sequence at this point, but its raw callback is
// dispatched before typed callbacks, which lets us bind the sequence to the
// admission callback below.
func (c *gatewayCursor) observe(event *discordgo.Event) {
	if !c.enabled() || event == nil || event.Operation != 0 || event.Sequence <= 0 {
		return
	}
	c.mu.Lock()
	entry := &gatewayCursorEntry{
		sequence:     event.Sequence,
		typeName:     event.Type,
		needsHandler: event.Type == "MESSAGE_CREATE" || event.Type == "INTERACTION_CREATE",
		skip:         event.Sequence <= c.cursor,
	}
	entry.handled = !entry.needsHandler
	c.queue = append(c.queue, entry)
	c.mu.Unlock()
	c.flush()
}

// begin claims the oldest unhandled inbound event. If a prior event failed,
// later callbacks are held behind it rather than allowing the monotonic cursor
// to jump over an unadmitted message.
func (c *gatewayCursor) begin(typeName string) (process, tracked bool) {
	if !c.enabled() {
		return true, false
	}
	c.mu.Lock()
	for _, entry := range c.queue {
		if !entry.needsHandler || entry.handled {
			continue
		}
		if entry.started {
			c.mu.Unlock()
			return false, true
		}
		if entry.typeName != typeName {
			c.mu.Unlock()
			return false, true
		}
		entry.started = true
		if entry.skip {
			entry.handled = true
			c.mu.Unlock()
			c.flush()
			return false, true
		}
		c.mu.Unlock()
		return true, true
	}
	c.mu.Unlock()
	// Direct unit tests and non-gateway callers invoke the callback without the
	// raw gateway event. Preserve that seam, while real gateway callbacks remain
	// tracked by the queue above.
	return true, false
}

func (c *gatewayCursor) finish(typeName string, admitted bool) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	for _, entry := range c.queue {
		if entry.needsHandler && entry.started && !entry.handled {
			if entry.typeName == typeName && admitted {
				entry.handled = true
			}
			break
		}
	}
	c.mu.Unlock()
	if admitted {
		c.flush()
	}
}

// flush persists contiguous completed gateway events. A failed store write
// leaves the completed entries in the queue so the next gateway event retries
// the same durable acknowledgement instead of silently advancing in memory.
func (c *gatewayCursor) flush() {
	if !c.enabled() {
		return
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	for {
		c.mu.Lock()
		if len(c.queue) == 0 || !c.queue[0].handled {
			c.mu.Unlock()
			return
		}
		entry := c.queue[0]
		alreadyPersisted := entry.sequence <= c.cursor
		c.mu.Unlock()

		if !alreadyPersisted {
			c.mu.Lock()
			ctx := c.ctx
			c.mu.Unlock()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := c.store.AdvanceIngressCursor(ctx, channel.PlatformDiscord, c.channelID, c.streamKey, entry.sequence); err != nil {
				logger().Warn("advance Discord ingress cursor failed", "sequence", entry.sequence, "error", err)
				return
			}
		}

		c.mu.Lock()
		if len(c.queue) > 0 && c.queue[0] == entry {
			if entry.sequence > c.cursor {
				c.cursor = entry.sequence
			}
			c.queue = c.queue[1:]
		}
		c.mu.Unlock()
	}
}
