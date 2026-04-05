package channel

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// WelcomeMessage is the shared welcome/help text for all channels.
const WelcomeMessage = "Hi! I'm Anna -- your local AI assistant.\n\n" +
	"Commands:\n" +
	"/new -- Start a fresh session\n" +
	"/compact -- Compress conversation history\n" +
	"/model -- Switch between models\n" +
	"/agent -- List or switch agents\n" +
	"/whoami -- Show your user ID\n\n" +
	"Just send me a message to get started."

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
