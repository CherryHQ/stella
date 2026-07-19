package channel

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/pkg/ai"
)

// WelcomeMessage is the shared welcome/help text for all channels.
// It explains both slash commands and natural-language shortcuts for
// channels without slash-command menus (like SMS or simple webhooks).
const WelcomeMessage = `Hi! I'm Stella — your local AI assistant.

You can chat normally or use slash commands.
Some agents/channels also support a few short natural-language controls.
For those shortcuts, keep them short and command-like.

Session control
  /new       Compact conversation context (same as /compact)
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

// FileReceivedContent returns the standard content block telling the agent
// about a file that has been saved to disk, with an Xberg extraction hint.
// assetsDir is the host-side assets directory; savedPath is the host-side absolute
// path returned by SaveAsset. The hint uses a path relative to the user root
// (parent of assetsDir) so it resolves correctly inside the bwrap sandbox at /workspace.
func FileReceivedContent(fileName, assetsDir, savedPath string) []ai.ContentBlock {
	displayPath := savedPath
	if assetsDir != "" {
		if rel, err := filepath.Rel(filepath.Dir(assetsDir), savedPath); err == nil {
			displayPath = rel
		}
	}
	return TextContent(fmt.Sprintf(
		"[File: %s — saved to %s]\n Read Xberg skill and use `xberg extract %q` to read its content.",
		fileName, displayPath, displayPath,
	))
}
