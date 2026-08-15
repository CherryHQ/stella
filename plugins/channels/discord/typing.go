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
			state := b.typing[channelID]
			if state == nil {
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
