package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	maxTelegramRetryAfter    = 5 * time.Second
	maxTelegramRetryAttempts = 1
)

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

// sendGroupDocument sends one document from a per-attempt source factory —
// FromReader over the bytes the outbox stored with the op. A fresh source per
// retry keeps a failed upload from resending a drained reader.
func (b *Bot) sendGroupDocument(ctx context.Context, chat tele.Recipient, newSrc func() tele.File, name string, opts *tele.SendOptions) error {
	return retryTelegram(ctx, func() error {
		_, err := b.bot.Send(chat, &tele.Document{File: newSrc(), FileName: name}, opts)
		return err
	})
}

// sendTelegramMarkdown sends one message: markdown first, plain fallback.
// check — when non-nil — re-validates channel ownership before every SDK
// call: the durable op path passes the op's lease guard so a fenced-out
// owner cannot fire the plain fallback after losing the channel. A flood or
// transport failure returns without falling back — the platform did not
// answer, so the markdown send may still land and a plain retry could
// duplicate it.
func (b *Bot) sendTelegramMarkdown(ctx context.Context, chat tele.Recipient, text string, opts *tele.SendOptions, check func(context.Context) error) (*tele.Message, error) {
	rendered := renderMarkdown(b.md, text)
	msg, err := b.sendTelegramText(ctx, chat, rendered, opts, check)
	if err == nil || isTelegramFlood(err) || isTelegramTransport(err) {
		return msg, err
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return nil, err
		}
	}
	plain := *opts
	plain.ParseMode = ""
	return b.sendTelegramText(ctx, chat, text, &plain, check)
}

// isTelegramFlood reports a Telegram rate-limit answer: the platform provably
// recorded nothing, so the op retries later as markdown rather than firing an
// immediate plain fallback into the same throttle.
func isTelegramFlood(err error) bool {
	var flood tele.FloodError
	return errors.As(err, &flood)
}

// isTelegramTransport reports whether err means the platform never answered —
// network failure or context cancellation. Only a real API response proves
// the send did not land, so transport failures must not trigger fallbacks.
func isTelegramTransport(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (b *Bot) sendTelegramText(ctx context.Context, chat tele.Recipient, text string, opts *tele.SendOptions, check func(context.Context) error) (*tele.Message, error) {
	var result *tele.Message
	err := retryTelegram(ctx, func() error {
		if check != nil {
			if err := check(ctx); err != nil {
				return err
			}
		}
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
