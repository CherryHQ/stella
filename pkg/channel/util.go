package channel

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
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
  /new       Start a fresh session (previous history leaves memory search)
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
// onto a fresh session. The previous session is archived and retained for
// explicit Session inspection, but excluded from memory search.
const NewSessionStartedMessage = "Started a fresh session. Previous history was archived and removed from memory search."

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

// AttachmentReceivedContent returns ephemeral blocks for an inbound attachment
// that has already been saved under the supplied immutable assets path. Small
// images remain provider-ready until admission canonicalizes them; files and
// oversized images retain their bytes only until admission persists immutable
// media and replaces the block with FileRefContent.
func AttachmentReceivedContent(fileName, savedPath string, data []byte) []ai.ContentBlock {
	mime := tools.DetectImageMime(data)
	if mime == "" {
		return []ai.ContentBlock{ai.FileContent{
			Data: append([]byte(nil), data...), MimeType: http.DetectContentType(data),
			Name: fileName, Path: savedPath,
		}}
	}
	displayPath := savedPath
	if len(data) > ai.MaxImageInputBytes {
		return []ai.ContentBlock{ai.FileContent{
			Data: append([]byte(nil), data...), MimeType: mime, Name: fileName, Path: displayPath,
		}}
	}
	return []ai.ContentBlock{
		ai.TextContent{Text: fmt.Sprintf("[Image: %s — saved to %s]", fileName, displayPath)},
		ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
	}
}

// InlineImageFallback returns ephemeral content for an inbound image that could
// not be persisted to the user's assets. Small images can still be converted to
// immutable session media by durable admission. Larger or unrecognized payloads
// retain their bytes in an unadmittable FileContent: they must be rejected rather
// than acknowledged as a text-only turn that silently lost its attachment.
func InlineImageFallback(fileName, mime string, data []byte) []ai.ContentBlock {
	if mime != "" && len(data) <= ai.MaxImageInputBytes {
		return []ai.ContentBlock{
			ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
		}
	}
	return []ai.ContentBlock{ai.FileContent{
		Data: append([]byte(nil), data...), MimeType: mime, Name: fileName,
	}}
}

// AttachmentUnavailableContent marks an adapter attachment whose bytes could
// not be obtained. Its empty Path and Data intentionally fail durable admission,
// preventing a caption or placeholder from being accepted without the attachment.
func AttachmentUnavailableContent(fileName string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.FileContent{Name: fileName}}
}

// ImmutableAssetPath returns the only logical path an accepted file attachment
// may advertise. The digest binds the path to its immutable bytes; sanitizing the
// basename keeps adapter-provided names within the assets root.
func ImmutableAssetPath(fileName string, data []byte) string {
	name := path.Base(strings.ReplaceAll(fileName, "\\", "/"))
	if name == "." || name == "" {
		name = "attachment"
	}
	digest := sha256.Sum256(data)
	return "$STELLA_ASSETS_DIR/" + hex.EncodeToString(digest[:]) + "-" + name
}

// AttachmentSaveFailureContent returns ephemeral blocks for an attachment whose
// adapter-side assets write failed. Small images can still be canonicalized as
// session media. Files retain an empty Path so durable admission rejects them:
// an adapter must not acknowledge a file that Xberg could not later open from
// the advertised assets path.
func AttachmentSaveFailureContent(fileName string, data []byte) []ai.ContentBlock {
	if mime := tools.DetectImageMime(data); mime != "" {
		return InlineImageFallback(fileName, mime, data)
	}
	return []ai.ContentBlock{ai.FileContent{
		Data: append([]byte(nil), data...), MimeType: http.DetectContentType(data), Name: fileName,
	}}
}

// FileReceivedContent returns the standard content block telling the agent
// about a file that has been saved to durable storage, with an Xberg extraction
// hint. savedPath is a portable logical path, never a host path.
func FileReceivedContent(fileName, savedPath string) []ai.ContentBlock {
	displayPath := savedPath
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
