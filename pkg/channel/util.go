package channel

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// WelcomeMessage is the shared welcome/help text for all channels.
// It explains both slash commands and natural-language shortcuts for
// channels without slash-command menus (like SMS or simple webhooks).
const WelcomeMessage = `Hi! I'm Stella — your local AI assistant.

You can chat normally or use slash commands.
Some agents/channels also support a few short natural-language controls.
For those shortcuts, keep them short and command-like.

Session control
  /new       Start a fresh session (previous history stays searchable)
             Direct messages only — a group's shared session cannot be reset
  /compact   Compress the current session in place (same session, shorter context)
             When enabled, short phrases like: "compact", "summarize history", "压缩会话", "总结历史"
  /abort     Cancel the in-progress reply
             When enabled, short phrases like: "abort", "cancel", "取消", "停止回复"
  /help      Show this help message
             When enabled, short phrases like: "help", "what can you do", "帮助"

Other commands
  /whoami    Show your user ID

If a short phrase is unclear, Stella treats it as a normal chat message.
Just send a message to get started.`

// GroupCompactUnsupportedMessage is the shared reply when a user asks to compact
// a group chat. Group memory is assembled from the shared event log rather than a
// per-agent LCM conversation, so manual compaction does not apply. Both the web
// group endpoint and the shared channel command handler use this text.
const GroupCompactUnsupportedMessage = "⚠️ Group memory is managed automatically from the shared event log, so manual compaction is not available in group chats."

// NewSessionStartedMessage is the shared reply after `/new` rotated the chat
// onto a fresh session. The previous session is archived, not deleted, so it
// stays reachable through cross-session memory search.
const NewSessionStartedMessage = "Started a fresh session. Previous history stays searchable via memory."

// SessionAlreadyResetMessage is the shared reply when a `/new` arrives after
// another one already rotated the same chat, so it has nothing left to do.
const SessionAlreadyResetMessage = "Session was already reset."

// NewSessionNotExecutedMessage is the reply when a rotation's commit
// acknowledgement was lost but re-reading the session binding proves it never
// moved: the transaction rolled back, so nothing was reset and the command is
// safe to send again.
const NewSessionNotExecutedMessage = "⚠️ Starting a new session did not go through — nothing was reset. Send /new again to retry."

// NewSessionOutcomeUnknownMessage is the reply when a rotation's commit
// acknowledgement was lost AND the follow-up read of the session binding also
// failed, so whether the reset happened is genuinely unknown. It must not invite
// a retry: if the reset did land, a second `/new` would archive the fresh
// context instead of the old one.
const NewSessionOutcomeUnknownMessage = "⚠️ Could not confirm whether the new session started. Do not send /new again yet — check whether this chat still remembers the earlier conversation first."

// NewSessionUnverifiableMessage is the reply when a `/new` arrives on a
// delivery that carries no stable message id. Without an identity the receipt
// cannot tell a redelivery from a new command, and `/new` is destructive, so
// it fails closed instead of running unguarded.
const NewSessionUnverifiableMessage = "⚠️ Cannot start a new session: this delivery has no message id, so a duplicate could not be detected. The session was not reset."

// GroupNewSessionUnsupportedMessage is the reply when `/new` arrives in a group
// chat. A group's context is shared by every member, so one member's chat
// command must not clear it for everyone. The refusal is explicit rather than
// silent: a `/new` that looked accepted but reset nothing would leave the group
// believing the context was cleared.
const GroupNewSessionUnsupportedMessage = "⚠️ Group sessions are shared, so `/new` cannot reset them. Use `/new` in a direct message to reset your own session."

// SplitMessage splits text into chunks that fit within maxLen.
// It tries to split at newline boundaries and avoids cutting multi-byte
// UTF-8 characters.
func SplitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		cutAt := maxLen
		// Avoid splitting in the middle of a multi-byte UTF-8 character.
		for cutAt > 0 && !utf8.RuneStart(text[cutAt]) {
			cutAt--
		}
		if idx := strings.LastIndex(text[:cutAt], "\n"); idx > 0 {
			cutAt = idx + 1 // Include the newline in the current chunk.
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}

	return chunks
}

// FormatDuration formats a duration as a human-friendly string.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm%ds", m, s)
	}
}

// ParseCommandArgs extracts arguments after the command token.
// Example: ParseCommandArgs("/command foo", "/command") returns "foo".
func ParseCommandArgs(text, cmd string) string {
	return strings.TrimSpace(strings.TrimPrefix(text, cmd))
}

// ParseSlashCommand extracts a slash command and its arguments.
// The returned command is always lowercased.
func ParseSlashCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	return strings.ToLower(fields[0]), ParseCommandArgs(text, fields[0])
}

// TextContent wraps a plain string as a single-element []ai.ContentBlock.
func TextContent(text string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.TextContent{Text: text}}
}

// AttachmentReceivedContent returns the content blocks for an inbound
// attachment that has been saved to disk. Images are presented as a saved-path
// note plus the inline image so the model sees the pixels immediately and can
// still reach the file later; everything else gets the Xberg extraction hint
// via FileReceivedContent. data is the raw file content used for image
// detection and inlining.
func AttachmentReceivedContent(fileName, assetsDir, savedPath string, data []byte) []ai.ContentBlock {
	mime := tools.DetectImageMime(data)
	if mime == "" {
		return FileReceivedContent(fileName, assetsDir, savedPath)
	}
	displayPath := attachmentDisplayPath(assetsDir, savedPath)
	if len(data) > ai.MaxImageInputBytes {
		return TextContent(fmt.Sprintf(
			"[Image: %s — saved to %s]\n The image is too large to attach inline; use the `read` tool on that path to view it.",
			fileName, displayPath,
		))
	}
	return []ai.ContentBlock{
		ai.TextContent{Text: fmt.Sprintf("[Image: %s — saved to %s]", fileName, displayPath)},
		ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
	}
}

// InlineImageFallback returns the content blocks for an inbound image that could
// not be persisted to the user's assets, so the turn still reaches the agent
// without a saved path. It honors the same inline ceiling as
// AttachmentReceivedContent: images within the shared ingestion ceiling are
// attached for downstream canonical or group handling; larger images degrade
// to an explicit text note naming the file and its size. Callers pass
// the mime they already detected; an empty mime is treated as non-inlineable.
func InlineImageFallback(fileName, mime string, data []byte) []ai.ContentBlock {
	if mime != "" && len(data) <= ai.MaxImageInputBytes {
		return []ai.ContentBlock{
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
		}
	}
	return TextContent(fmt.Sprintf(
		"[Image: %s — received but could not be stored or attached inline (%s).]",
		fileName, humanReadableSize(len(data)),
	))
}

// AttachmentSaveFailureContent returns the blocks to route to the agent when an
// inbound attachment downloaded successfully but could not be persisted. It
// keeps the four channel plugins symmetric on the save-failure path: image
// bytes degrade via InlineImageFallback (attached within the ingestion ceiling,
// else a text note) and any other file gets an explicit placeholder so the turn is never
// silently dropped. data is the raw downloaded bytes used for image detection.
func AttachmentSaveFailureContent(fileName string, data []byte) []ai.ContentBlock {
	if mime := tools.DetectImageMime(data); mime != "" {
		return InlineImageFallback(fileName, mime, data)
	}
	return TextContent(fmt.Sprintf(
		"[File: %s — received but could not be stored (%s).]",
		fileName, humanReadableSize(len(data)),
	))
}

// humanReadableSize renders a byte count using binary units (B, KiB, MiB, …)
// for the attachment fallback notes.
func humanReadableSize(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := int64(n) / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FileReceivedContent returns the standard content block telling the agent
// about a file that has been saved to disk, with an Xberg extraction hint.
// assetsDir is the host-side assets directory; savedPath is the host-side absolute
// path returned by SaveAsset. The hint uses a path relative to the user root
// (parent of assetsDir) so it resolves correctly inside the bwrap sandbox at /workspace.
func FileReceivedContent(fileName, assetsDir, savedPath string) []ai.ContentBlock {
	displayPath := attachmentDisplayPath(assetsDir, savedPath)
	return TextContent(fmt.Sprintf(
		"[File: %s — saved to %s]\n Read Xberg skill and use `xberg extract %q` to read its content.",
		fileName, displayPath, displayPath,
	))
}

// ImageFileName builds a file name for an inbound image message that carries no
// client-supplied name, deriving the extension from the detected MIME type.
func ImageFileName(base, mime string) string {
	if base == "" {
		base = "image"
	}
	ext := strings.TrimPrefix(mime, "image/")
	switch ext {
	case "", mime:
		ext = "bin"
	case "jpeg":
		ext = "jpg"
	}
	return base + "." + ext
}

// attachmentDisplayPath rewrites a host-side saved path relative to the user
// root (parent of assetsDir) so it resolves inside the bwrap sandbox at
// /workspace; without an assetsDir the host path is shown as-is.
func attachmentDisplayPath(assetsDir, savedPath string) string {
	if assetsDir == "" {
		return savedPath
	}
	if rel, err := filepath.Rel(filepath.Dir(assetsDir), savedPath); err == nil {
		return rel
	}
	return savedPath
}
