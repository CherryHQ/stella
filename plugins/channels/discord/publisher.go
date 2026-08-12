package discord

import (
	"context"
	"strings"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
)

func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) error {
	text, images, files, err := collectResponse(ctx, req.Stream)
	if err != nil {
		if text != "" {
			text += "\n\n"
		}
		text += "Agent error: " + err.Error()
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 && len(files) == 0 {
		text = "(empty response)"
	}
	if text != "" {
		if err := b.sendText(ctx, req.PlatformGroupID, text, req.ReplyTo); err != nil {
			return err
		}
	}
	for _, image := range images {
		if err := b.sendImage(req.PlatformGroupID, image); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := b.sendFile(req.PlatformGroupID, file); err != nil {
			return err
		}
	}
	return nil
}
