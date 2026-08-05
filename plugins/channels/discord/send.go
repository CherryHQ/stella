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

func (b *Bot) sendText(ctx context.Context, channelID, text, replyTo string) error {
	return b.sendTextOptions(ctx, channelID, text, replyTo, false)
}

func (b *Bot) sendTextOptions(ctx context.Context, channelID, text, replyTo string, silent bool) error {
	chunks := channel.SplitMessage(text, maxMessageLength)
	for i, chunk := range chunks {
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
		if _, err := b.session.ChannelMessageSendComplex(channelID, msg); err != nil {
			return fmt.Errorf("send discord message: %w", err)
		}
	}
	return nil
}

func (b *Bot) sendImage(channelID string, image channel.ImageEvent) error {
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		return fmt.Errorf("decode discord image: %w", err)
	}
	ext := ".png"
	if exts, _ := mime.ExtensionsByType(image.MimeType); len(exts) > 0 {
		ext = exts[0]
	}
	_, err = b.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{AllowedMentions: noMentions(), Files: []*discordgo.File{{Name: "image" + ext, Reader: bytes.NewReader(data)}}})
	return err
}

func (b *Bot) sendFile(channelID string, file channel.FileEvent) error {
	f, err := os.Open(file.Path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}
	_, err = b.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{AllowedMentions: noMentions(), Files: []*discordgo.File{{Name: name, Reader: f}}})
	return err
}
