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
             When enabled, short phrases like: "new session", "start over", "新会话", "重新开始"
  /compact   Compact conversation context
             When enabled, short phrases like: "compact", "summarize history", "压缩会话", "总结历史"
  /abort     Cancel the in-progress reply
             When enabled, short phrases like: "abort", "cancel", "取消", "停止回复"
  /help      Show this help message
             When enabled, short phrases like: "help", "what can you do", "帮助"

Other commands
  /model     Switch between models
  /agent     List or switch agents
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

// GroupNewSessionUnavailableMessage is the reply when a group `/new` arrives at
// a deployment whose group plumbing (group identity resolution or the member
// roster) is not wired, so there is no roster to rotate against.
const GroupNewSessionUnavailableMessage = "⚠️ Starting a fresh session is not available in this group."

// GroupNewSessionNoAgentsMessage is the reply for a `/new` in a group that has
// no agent members, so there is no session to reset.
const GroupNewSessionNoAgentsMessage = "⚠️ This group has no agents, so there is no session to reset."

// GroupNewSessionUsageMessage is the reply for an ambiguous group `/new`. Each
// agent in a group keeps its own session, so a multi-agent group requires an
// explicit target: resetting every agent's context on an unclear command would
// be destructive by default.
func GroupNewSessionUsageMessage(agentIDs []string) string {
	var b strings.Builder
	b.WriteString("⚠️ This group has more than one agent, so `/new` needs a target.\nUse `/new @agent`:")
	for _, id := range agentIDs {
		b.WriteString("\n  @")
		b.WriteString(id)
	}
	return b.String()
}

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
// Example: ParseCommandArgs("/agent foo", "/agent") returns "foo".
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

// maxInlineAttachmentImageBytes caps raw image bytes inlined into the
// conversation, matching the read tool's provider inline-image ceiling.
// Larger images are presented as a saved path with a read-tool hint so the
// read tool's resize and non-vision fallback logic takes over.
const maxInlineAttachmentImageBytes = 5 * 1024 * 1024

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
	if len(data) > maxInlineAttachmentImageBytes {
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
// AttachmentReceivedContent: images within maxInlineAttachmentImageBytes are
// attached inline so the model sees the pixels; larger images cannot be stored
// or inlined (they would balloon the request past the provider limit), so they
// degrade to an explicit text note naming the file and its size. Callers pass
// the mime they already detected; an empty mime is treated as non-inlineable.
func InlineImageFallback(fileName, mime string, data []byte) []ai.ContentBlock {
	if mime != "" && len(data) <= maxInlineAttachmentImageBytes {
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
// bytes degrade via InlineImageFallback (inline within the ceiling, else a text
// note) and any other file gets an explicit placeholder so the turn is never
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
