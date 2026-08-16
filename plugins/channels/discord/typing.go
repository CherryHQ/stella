package discord

import (
	"context"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type typingState struct {
	refs   int
	cancel context.CancelFunc
}

// startTypingHeartbeat shares one Discord typing loop per concrete target.
// Concurrent turns acquire references instead of multiplying REST traffic.
func (b *Bot) startTypingHeartbeat(channelID string) context.CancelFunc {
	if b.rest == nil || channelID == "" {
		return func() {}
	}
	b.typingMu.Lock()
	state := b.typing[channelID]
	if state == nil {
		ctx, cancel := context.WithCancel(context.Background())
		state = &typingState{cancel: cancel}
		b.typing[channelID] = state
		go runTypingHeartbeat(ctx, typingInterval, func() {
			if err := b.rest.ChannelTyping(channelID, discordgo.WithContext(ctx)); err != nil && ctx.Err() == nil {
				logger().Debug("renew Discord typing indicator failed", "channel_id", channelID, "error", err)
			}
		})
	}
	state.refs++
	b.typingMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.typingMu.Lock()
			defer b.typingMu.Unlock()
			// Release only the *typingState captured at acquire time. If the map
			// entry has since rolled over to a new heartbeat generation (this one
			// already dropped to zero refs and was replaced), that generation owns
			// its own refcount and this release is a stale no-op.
			if b.typing[channelID] != state {
				return
			}
			state.refs--
			if state.refs == 0 {
				state.cancel()
				delete(b.typing, channelID)
			}
		})
	}
}

func (b *Bot) stopTypingHeartbeats() {
	b.typingMu.Lock()
	defer b.typingMu.Unlock()
	for channelID, state := range b.typing {
		state.cancel()
		delete(b.typing, channelID)
	}
}

func runTypingHeartbeat(ctx context.Context, interval time.Duration, send func()) {
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}
