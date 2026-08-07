package discord

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

// Publish renders an agent response stream to a Discord channel. It is the
// GroupPublisher egress registered in New; the assertion block there and this
// file go together (M1). MVP routes through the direct-answer path (IsGroup
// false), so Publish is exercised only if group dispatch is later enabled.
func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	chatID := req.PlatformGroupID
	if chatID == "" {
		return fmt.Errorf("discord: empty group id")
	}

	response, images, err := collectDiscordResponse(ctx, req.Stream)
	if err != nil {
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", err)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", err)
		}
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}

	// Reply to the triggering message when one is set; otherwise post plainly.
	// Both paths suppress mentions (H1).
	if req.ReplyTo != "" {
		if _, err := b.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
			Content:         response,
			AllowedMentions: suppressMentions(),
			Reference: &discordgo.MessageReference{
				MessageID: req.ReplyTo,
				ChannelID: chatID,
			},
		}); err != nil {
			return err
		}
	} else if err := b.sendChunked(chatID, response); err != nil {
		return err
	}
	for _, img := range images {
		b.sendImageTo(chatID, img)
	}
	return nil
}

func collectDiscordResponse(ctx context.Context, stream *channel.ChatStream) (string, []channel.ImageEvent, error) {
	if stream == nil {
		return "", nil, nil
	}
	var sb strings.Builder
	var streamErr error
	var images []channel.ImageEvent
	tracker := newToolTracker()
	for {
		select {
		case evt, ok := <-stream.Events:
			if !ok {
				response := sb.String()
				if tracker.HasHistory() {
					response += tracker.RenderFinal()
				}
				return response, images, streamErr
			}
			if evt.Err != nil {
				streamErr = evt.Err
				continue
			}
			if evt.Image != nil {
				images = append(images, *evt.Image)
				continue
			}
			if evt.ToolUse != nil {
				tracker.Handle(evt.ToolUse)
				continue
			}
			sb.WriteString(evt.Text)
		case <-ctx.Done():
			return sb.String(), images, ctx.Err()
		}
	}
}
