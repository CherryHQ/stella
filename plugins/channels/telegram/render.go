package telegram

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
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

// sendFinalResponse sends the completed response with markdown rendering,
// splitting into chunks if necessary. It also sends any collected images.
func (b *Bot) sendFinalResponse(c tele.Context, response string, images []channel.ImageEvent) {
	if err := b.sendChunkedMarkdown(c.Chat(), response, false, nil); err != nil {
		logger().Error("sendFinalResponse failed", "chat_id", c.Chat().ID, "error", err)
	}
	for _, img := range images {
		b.sendImage(c, img)
	}
}

// sendImage decodes a base64 image and sends it as a photo to the chat.
func (b *Bot) sendImage(c tele.Context, img channel.ImageEvent) {
	b.sendImageTo(c.Chat(), img)
}

func (b *Bot) sendImageTo(chat tele.Recipient, img channel.ImageEvent) {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		logger().Error("decode image failed", "error", err)
		return
	}
	photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(data))}
	if _, err := b.bot.Send(chat, photo); err != nil {
		logger().Error("send image failed", "error", err)
	}
}

// sendChunkedMarkdown splits text into chunks, renders each as Telegram
// MarkdownV2, and falls back to plain text on error. If sendOpts is non-nil
// it is used for the markdown send attempt. Returns the first send error
// that could not be recovered via plain-text fallback.
func (b *Bot) sendChunkedMarkdown(chat tele.Recipient, text string, silent bool, sendOpts *tele.SendOptions) error {
	if sendOpts == nil {
		sendOpts = &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	}
	chunks := channel.SplitMessage(text, telegramMaxMessageLen)
	for _, chunk := range chunks {
		rendered := renderMarkdown(b.md, chunk)
		if _, err := b.bot.Send(chat, rendered, sendOpts); err != nil {
			logger().Warn("markdown send failed, falling back to plain text", "error", err)
			plainOpts := &tele.SendOptions{DisableNotification: silent}
			if _, err := b.bot.Send(chat, chunk, plainOpts); err != nil {
				return fmt.Errorf("send message: %w", err)
			}
		}
	}
	return nil
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
