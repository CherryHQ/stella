package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	maxTelegramRetryAfter    = 5 * time.Second
	maxTelegramRetryAttempts = 1
)

// Publish renders the dispatcher-owned ChatStream as one Telegram message.
// It deliberately has no session or agent logic: a failed platform request is
// returned so the existing at-least-once group dispatcher owns the retry.
func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) (err error) {
	stream, err := internalchannel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	req.Stream = stream
	chatID, err := strconv.ParseInt(req.PlatformGroupID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid group id %q: %w", req.PlatformGroupID, err)
	}
	chat := &tele.Chat{ID: chatID}
	opts, threadID, err := telegramGroupSendOptions(req, chatID)
	if err != nil {
		return err
	}

	typingCtx, stopTyping := context.WithCancel(ctx)
	defer stopTyping()
	go keepGroupTyping(typingCtx, b.bot, chat, threadID)

	// A rejected replay never reaches this point. Egress failure clears the
	// acknowledgement; the dispatcher owns retries and terminal delivery state.
	b.react(req.PlatformGroupID, req.ReplyTo, reactionReceived)
	defer func() { b.finishReaction(req.PlatformGroupID, req.ReplyTo, err == nil) }()

	response, images, files := collectGroupReplay(req.Stream)
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}

	for _, chunk := range channel.SplitMessage(response, telegramMaxMessageLen) {
		if _, err := b.sendTelegramMarkdown(ctx, chat, chunk, opts); err != nil {
			return fmt.Errorf("telegram: send response: %w", err)
		}
	}
	for _, img := range images {
		if err := b.sendGroupImage(ctx, chat, img, opts); err != nil {
			return fmt.Errorf("telegram: send response image: %w", err)
		}
	}
	for _, file := range files {
		if err := b.sendGroupFile(ctx, chat, file, opts); err != nil {
			return fmt.Errorf("telegram: send response file: %w", err)
		}
	}
	return nil
}

func telegramGroupSendOptions(req internalchannel.GroupPublishRequest, chatID int64) (*tele.SendOptions, int, error) {
	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	threadID := 0
	if req.PlatformThreadID != "" {
		var err error
		threadID, err = strconv.Atoi(req.PlatformThreadID)
		if err != nil {
			return nil, 0, fmt.Errorf("telegram: invalid thread id %q: %w", req.PlatformThreadID, err)
		}
		opts.ThreadID = threadID
	}
	if req.ReplyTo != "" {
		replyID, err := strconv.Atoi(req.ReplyTo)
		if err != nil {
			return nil, 0, fmt.Errorf("telegram: invalid reply_to %q: %w", req.ReplyTo, err)
		}
		// telebot v4 does not yet serialize SendOptions.ReplyParams. ReplyTo
		// preserves the same-chat reply anchor and AllowWithoutReply retains
		// the existing best-effort delivery behavior.
		opts.ReplyTo = &tele.Message{ID: replyID, Chat: &tele.Chat{ID: chatID}}
		opts.AllowWithoutReply = true
	}
	return opts, threadID, nil
}

// collectGroupReplay folds the validated replay into the one message this
// publisher sends. The dispatcher buffers the whole turn before egress, so the
// stream is already closed and complete: there is nothing to stream, and no
// error left to surface -- ValidateGroupReplay rejected the turn if it failed.
func collectGroupReplay(stream *channel.ChatStream) (string, []channel.ImageEvent, []channel.FileEvent) {
	if stream == nil {
		return "", nil, nil
	}
	var text strings.Builder
	tracker := newToolTracker()
	var images []channel.ImageEvent
	var files []channel.FileEvent
	for event := range stream.Events {
		switch {
		case event.Image != nil:
			images = append(images, *event.Image)
		case event.File != nil:
			files = append(files, *event.File)
		default:
			if event.ToolUse != nil {
				tracker.Handle(event.ToolUse)
			}
			text.WriteString(event.Text)
		}
	}
	response := text.String()
	if tracker.HasHistory() {
		response += tracker.RenderFinal()
	}
	return response, images, files
}

func (b *Bot) sendGroupImage(ctx context.Context, chat tele.Recipient, img channel.ImageEvent, opts *tele.SendOptions) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	return retryTelegram(ctx, func() error {
		// Multipart uploads consume their reader. Construct a new photo for each
		// bounded FloodError retry rather than uploading an exhausted reader.
		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(data))}
		_, err := b.bot.Send(chat, photo, opts)
		return err
	})
}

func (b *Bot) sendGroupFile(ctx context.Context, chat tele.Recipient, file channel.FileEvent, opts *tele.SendOptions) error {
	name := file.Name
	if name == "" {
		name = "file"
	}
	return retryTelegram(ctx, func() error {
		_, err := b.bot.Send(chat, &tele.Document{File: tele.FromDisk(file.Path), FileName: name}, opts)
		return err
	})
}

func keepGroupTyping(ctx context.Context, bot *tele.Bot, chat tele.Recipient, threadID int) {
	notify := func() {
		if threadID != 0 {
			_ = bot.Notify(chat, tele.Typing, threadID)
			return
		}
		_ = bot.Notify(chat, tele.Typing)
	}
	notify()
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notify()
		}
	}
}

func (b *Bot) sendTelegramMarkdown(ctx context.Context, chat tele.Recipient, text string, opts *tele.SendOptions) (*tele.Message, error) {
	rendered := renderMarkdown(b.md, text)
	msg, err := b.sendTelegramText(ctx, chat, rendered, opts)
	if err == nil || telegramRetryAfter(err) > 0 {
		return msg, err
	}
	plain := *opts
	plain.ParseMode = ""
	return b.sendTelegramText(ctx, chat, text, &plain)
}

func (b *Bot) sendTelegramText(ctx context.Context, chat tele.Recipient, text string, opts *tele.SendOptions) (*tele.Message, error) {
	var result *tele.Message
	err := retryTelegram(ctx, func() error {
		msg, err := b.bot.Send(chat, text, opts)
		result = msg
		return err
	})
	return result, err
}

func retryTelegram(ctx context.Context, send func() error) error {
	for attempt := 0; ; attempt++ {
		err := send()
		if err == nil || isTelegramNoopEdit(err) {
			return nil
		}
		retryAfter := telegramRetryAfter(err)
		if retryAfter <= 0 || retryAfter > maxTelegramRetryAfter || attempt >= maxTelegramRetryAttempts {
			return err
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func telegramRetryAfter(err error) time.Duration {
	var flood tele.FloodError
	if errors.As(err, &flood) && flood.RetryAfter > 0 {
		return time.Duration(flood.RetryAfter) * time.Second
	}
	return 0
}

func isTelegramNoopEdit(err error) bool {
	return errors.Is(err, tele.ErrMessageNotModified) ||
		errors.Is(err, tele.ErrSameMessageContent) ||
		strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}
