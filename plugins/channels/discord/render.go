package discord

import (
	"bytes"
	"encoding/base64"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// suppressMentions returns an AllowedMentions that disables all mention parsing
// (H1). Every outbound path uses it so relayed user text or LLM output can never
// ping @everyone/@here/roles/users.
func suppressMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

// sendPlain sends a single message, split into chunks if needed, with all
// mentions suppressed. Discord renders CommonMark natively, so the text is sent
// as-is (no markdown transformation).
func (b *Bot) sendPlain(chatID, text string) {
	if err := b.sendChunked(chatID, text); err != nil {
		logger().Error("sendPlain failed", "channel_id", chatID, "error", err)
	}
}

// sendChunked splits text at the Discord length limit and sends each chunk with
// mentions suppressed. Returns the first unrecoverable send error.
func (b *Bot) sendChunked(chatID, text string) error {
	chunks := channel.SplitMessage(text, discordMaxMessageLen)
	for _, chunk := range chunks {
		if _, err := b.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
			Content:         chunk,
			AllowedMentions: suppressMentions(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// sendFinalResponse sends the completed response, splitting into chunks if
// necessary, then sends any collected images.
func (b *Bot) sendFinalResponse(chatID, response string, images []channel.ImageEvent) {
	if err := b.sendChunked(chatID, response); err != nil {
		logger().Error("sendFinalResponse failed", "channel_id", chatID, "error", err)
	}
	for _, img := range images {
		b.sendImageTo(chatID, img)
	}
}

// sendImageTo decodes a base64 image and sends it as a file attachment.
func (b *Bot) sendImageTo(chatID string, img channel.ImageEvent) {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return
	}
	name := channel.ImageFileName("image", img.MimeType)
	if _, err := b.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
		Files: []*discordgo.File{{
			Name:        name,
			ContentType: img.MimeType,
			Reader:      bytes.NewReader(data),
		}},
		AllowedMentions: suppressMentions(),
	}); err != nil {
		logger().Error("send image failed", "error", err)
	}
}
