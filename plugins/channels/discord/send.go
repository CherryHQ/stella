package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func softReference(channelID, messageID string) *discordgo.MessageReference {
	if messageID == "" {
		return nil
	}
	strict := false
	return &discordgo.MessageReference{MessageID: messageID, ChannelID: channelID, FailIfNotExists: &strict}
}

// textDelivery is the resume half of a group text send: how many leading chunks
// a previous attempt already got onto Discord, and how to durably confirm the
// next one. The zero value is a plain one-shot send — direct messages and
// notifications have nothing to resume.
type textDelivery struct {
	cursor  int64
	confirm func(context.Context, int64) error
}

func (d textDelivery) delivered(ctx context.Context, n int) error {
	if d.confirm == nil {
		return nil
	}
	return d.confirm(ctx, int64(n))
}

func (b *Bot) sendText(ctx context.Context, channelID, text, replyTo string) error {
	return b.sendTextOptions(ctx, channelID, text, replyTo, false)
}

func (b *Bot) sendTextOptions(ctx context.Context, channelID, text, replyTo string, silent bool) error {
	return b.sendTextChunks(ctx, channelID, text, replyTo, silent, textDelivery{})
}

// sendTextChunks sends text as Discord-sized chunks, skipping the ones a
// previous attempt already delivered and confirming each one it lands. The
// split is deterministic for a given text, so a chunk index means the same
// thing across attempts — that is what makes the cursor safe to resume from.
func (b *Bot) sendTextChunks(ctx context.Context, channelID, text, replyTo string, silent bool, resume textDelivery) error {
	chunks := channel.SplitMarkdown(text, maxMessageLength)
	for i, chunk := range chunks {
		if int64(i) < resume.cursor {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		msg := &discordgo.MessageSend{Content: chunk, AllowedMentions: noMentions()}
		if silent {
			msg.Flags = discordgo.MessageFlagsSuppressNotifications
		}
		if i == 0 {
			msg.Reference = softReference(channelID, replyTo)
		}
		if b.rest == nil {
			return fmt.Errorf("send discord message: REST client unavailable")
		}
		if _, err := b.rest.ChannelMessageSendComplex(channelID, msg, discordgo.WithContext(ctx)); err != nil {
			return fmt.Errorf("send discord message chunk %d/%d: %w", i+1, len(chunks), err)
		}
		// Confirm before the next send: a failure here means the dispatch is no
		// longer ours, and continuing would race another owner's delivery.
		if err := resume.delivered(ctx, i+1); err != nil {
			return fmt.Errorf("confirm discord message chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

func (b *Bot) sendImage(ctx context.Context, channelID string, image channel.ImageEvent) error {
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		return fmt.Errorf("decode discord image: %w", err)
	}
	ext := ".png"
	if exts, _ := mime.ExtensionsByType(image.MimeType); len(exts) > 0 {
		ext = exts[0]
	}
	if b.rest == nil {
		return fmt.Errorf("send discord image: REST client unavailable")
	}
	_, err = b.rest.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{AllowedMentions: noMentions(), Files: []*discordgo.File{{Name: "image" + ext, Reader: bytes.NewReader(data)}}}, discordgo.WithContext(ctx))
	return err
}

func (b *Bot) sendFile(ctx context.Context, channelID string, file channel.FileEvent) error {
	f, err := os.Open(file.Path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}
	if b.rest == nil {
		return fmt.Errorf("send discord file: REST client unavailable")
	}
	_, err = b.rest.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{AllowedMentions: noMentions(), Files: []*discordgo.File{{Name: name, Reader: f}}}, discordgo.WithContext(ctx))
	return err
}
