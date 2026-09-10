package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yuin/goldmark/parser"
	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/goldmark/mdutil"
)

// goldmarkMD is the interface satisfied by the goldmark Markdown converter.
type goldmarkMD interface {
	Convert(source []byte, w io.Writer, opts ...parser.ParseOption) error
}

func (b *Bot) sendFinalResponseChecked(ctx context.Context, stream *channel.ChatStream, c tele.Context, response string, images []channel.ImageEvent, kind channel.SendKind) error {
	if err := b.sendChunkedMarkdownChecked(ctx, stream, c.Chat(), response, false, nil, kind); err != nil {
		return err
	}
	for _, img := range images {
		if err := b.sendImageChecked(ctx, stream, c, img); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) sendImageChecked(ctx context.Context, stream *channel.ChatStream, c tele.Context, img channel.ImageEvent) error {
	return b.sendImageTo(ctx, stream, c.Chat(), img)
}

func (b *Bot) sendImageTo(ctx context.Context, stream *channel.ChatStream, chat tele.Recipient, img channel.ImageEvent) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return err
	}
	photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(data))}
	if err := stream.AuthorizeSend(ctx, channel.SendOutput); err != nil {
		return err
	}
	if _, err := b.bot.Send(chat, photo); err != nil {
		logger().Error("send image failed", "error", err)
		return err
	}
	return nil
}

// sendChunkedMarkdown splits text into chunks, renders each as Telegram
// MarkdownV2, and falls back to plain text on error. If sendOpts is non-nil
// it is used for the markdown send attempt. Returns the first send error
// that could not be recovered via plain-text fallback.
func (b *Bot) sendChunkedMarkdown(chat tele.Recipient, text string, silent bool, sendOpts *tele.SendOptions) error {
	return b.sendChunkedMarkdownChecked(context.Background(), nil, chat, text, silent, sendOpts, channel.SendOutput)
}

func (b *Bot) sendChunkedMarkdownChecked(ctx context.Context, stream *channel.ChatStream, chat tele.Recipient, text string, silent bool, sendOpts *tele.SendOptions, kind channel.SendKind) error {
	if sendOpts == nil {
		sendOpts = &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	}
	chunks := channel.SplitMessage(text, telegramMaxMessageLen)
	for _, chunk := range chunks {
		rendered := renderMarkdown(b.md, chunk)
		if err := stream.AuthorizeSend(ctx, kind); err != nil {
			return err
		}
		if _, err := b.bot.Send(chat, rendered, sendOpts); err != nil {
			// Telegram rejects malformed MarkdownV2 before creating a message. The
			// SDK exposes that case as a typed 400 or its canonical formatted error,
			// so a plain-text retry is safe.
			// Every other managed error remains unknown: a second request could
			// duplicate a message accepted before the error reached us.
			if stream != nil && !isTelegramMarkdownRejected(err) {
				return fmt.Errorf("send markdown message: %w", err)
			}
			logger().Warn("markdown send failed, falling back to plain text", "error", err)
			plainOpts := &tele.SendOptions{DisableNotification: silent}
			if err := stream.AuthorizeSend(ctx, kind); err != nil {
				return err
			}
			if _, err := b.bot.Send(chat, chunk, plainOpts); err != nil {
				return fmt.Errorf("send message: %w", err)
			}
		}
	}
	return nil
}

func isTelegramMarkdownRejected(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *tele.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusBadRequest && strings.Contains(strings.ToLower(apiErr.Description), "can't parse entities")
	}
	// telebot returns unknown Bot API descriptions as a formatted error rather
	// than *tele.Error. Keep the match exact enough to cover that canonical
	// 400 rejection without treating arbitrary transport text as recoverable.
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.HasPrefix(message, "telegram: bad request: can't parse entities:") && strings.HasSuffix(message, "(400)")
}

// renderMarkdown converts standard markdown to Telegram MarkdownV2 format.
func renderMarkdown(md goldmarkMD, text string) string {
	text = normalizeForTelegram(text)
	var buf bytes.Buffer
	if err := md.Convert([]byte(text), &buf); err != nil {
		return text
	}
	result := buf.String()
	if result == "" {
		return text
	}
	return result
}

// normalizeForTelegram rewrites the markdown that the MarkdownV2 converter
// drops on the floor: it renders neither autolink nor HTML nodes, so
// <https://…> links and <details> sections would reach the user with their
// content silently missing.
func normalizeForTelegram(text string) string {
	return mdutil.ExpandAutoLinks(flattenDetails(text))
}

// flattenDetails unwraps <details> sections into a bold summary line followed
// by the body. Telegram's expandable blockquote would be the closer match, but
// the MarkdownV2 converter mangles multi-line quotes and cannot nest code
// blocks in them; revisit if that converter gains real blockquote support.
func flattenDetails(text string) string {
	sections := mdutil.FindDetails(text)
	if len(sections) == 0 {
		return text
	}

	var b strings.Builder
	prev := 0
	for _, d := range sections {
		b.WriteString(text[prev:d.Start])
		if d.Summary != "" {
			b.WriteString("**" + d.Summary + "**\n\n")
		}
		b.WriteString(d.Body)
		prev = d.End
	}
	b.WriteString(text[prev:])
	return b.String()
}
