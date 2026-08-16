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
	groupProgressPlaceholder = "Thinking…"
	maxTelegramRetryAfter    = 5 * time.Second
	maxTelegramRetryAttempts = 1
)

// Publish renders the dispatcher-owned ChatStream as one Telegram message.
// It deliberately has no session or agent logic: a failed platform request is
// returned so the existing at-least-once group dispatcher owns the retry.
func (b *Bot) Publish(ctx context.Context, req internalchannel.GroupPublishRequest) (err error) {
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

	progress, err := b.sendTelegramMarkdown(ctx, chat, groupProgressPlaceholder, opts)
	if err != nil {
		return fmt.Errorf("telegram: send progress: %w", err)
	}

	// Acknowledge only once the group turn is actually being served, and settle
	// the verdict on every exit below. A dispatcher retry re-runs this and
	// overwrites the emoji, which is exactly the behaviour we want.
	streamOK := false
	b.react(req.PlatformGroupID, req.ReplyTo, reactionReceived)
	defer func() { b.finishReaction(req.PlatformGroupID, req.ReplyTo, err == nil && streamOK) }()

	response, images, streamErr, err := b.renderGroupProgress(ctx, progress, opts, req.Stream)
	if err != nil {
		return err
	}
	if streamErr != nil {
		logger().Error("agent group stream error", "error", streamErr)
		response = appendGroupStreamFailure(response)
	}
	streamOK = streamErr == nil
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}

	chunks := channel.SplitMessage(response, telegramMaxMessageLen)
	if err := b.editTelegramMarkdown(ctx, progress, chunks[0], opts); err != nil {
		return fmt.Errorf("telegram: finalize progress: %w", err)
	}
	for _, chunk := range chunks[1:] {
		if _, err := b.sendTelegramMarkdown(ctx, chat, chunk, opts); err != nil {
			return fmt.Errorf("telegram: send response continuation: %w", err)
		}
	}
	for _, img := range images {
		if err := b.sendGroupImage(ctx, chat, img, opts); err != nil {
			return fmt.Errorf("telegram: send response image: %w", err)
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

// renderGroupProgress coalesces text and tool updates into roughly one edit per
// second. It never sends a duplicate display, and the final edit is performed
// by Publish after the complete tool summary is available.
func (b *Bot) renderGroupProgress(ctx context.Context, progress *tele.Message, opts *tele.SendOptions, stream *channel.ChatStream) (string, []channel.ImageEvent, error, error) {
	if stream == nil {
		return "", nil, nil, nil
	}
	var text strings.Builder
	tracker := newToolTracker()
	var images []channel.ImageEvent
	lastDisplay := groupProgressPlaceholder
	dirty := false
	ticker := time.NewTicker(streamEditInterval)
	defer ticker.Stop()

	flush := func() error {
		if !dirty {
			return nil
		}
		display := buildStreamDisplay(text.String(), tracker.Render(), tracker.IsDisplaying())
		if display == lastDisplay {
			dirty = false
			return nil
		}
		if err := b.editTelegramMarkdown(ctx, progress, display, opts); err != nil {
			return fmt.Errorf("telegram: update progress: %w", err)
		}
		lastDisplay = display
		dirty = false
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return text.String(), images, ctx.Err(), ctx.Err()
		case <-ticker.C:
			if err := flush(); err != nil {
				return text.String(), images, nil, err
			}
		case event, ok := <-stream.Events:
			if !ok {
				response := text.String()
				if tracker.HasHistory() {
					response += tracker.RenderFinal()
				}
				return response, images, nil, nil
			}
			if event.Err != nil {
				response := text.String()
				if tracker.HasHistory() {
					response += tracker.RenderFinal()
				}
				return response, images, event.Err, nil
			}
			if event.Image != nil {
				images = append(images, *event.Image)
				continue
			}
			if event.ToolUse != nil {
				tracker.Handle(event.ToolUse)
			}
			if event.Text != "" {
				text.WriteString(event.Text)
			}
			dirty = true
		}
	}
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

func appendGroupStreamFailure(response string) string {
	const failure = "The response could not be completed. Please try again."
	if strings.TrimSpace(response) == "" {
		return failure
	}
	return response + "\n\n" + failure
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

func (b *Bot) editTelegramMarkdown(ctx context.Context, msg *tele.Message, text string, opts *tele.SendOptions) error {
	rendered := renderMarkdown(b.md, text)
	err := b.editTelegramText(ctx, msg, rendered, opts)
	if err == nil || telegramRetryAfter(err) > 0 {
		return err
	}
	plain := *opts
	plain.ParseMode = ""
	return b.editTelegramText(ctx, msg, text, &plain)
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

func (b *Bot) editTelegramText(ctx context.Context, msg *tele.Message, text string, opts *tele.SendOptions) error {
	return retryTelegram(ctx, func() error {
		_, err := b.bot.Edit(msg, text, opts)
		return err
	})
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
