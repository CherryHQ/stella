package channel

import (
	"context"
	"fmt"
	"strings"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// HandleCommand processes common bot commands shared across all channels.
// /model and /agent are left to each channel because they need platform-specific UI.
func HandleCommand(ctx context.Context, rc *ResolvedChat, text, senderID string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start", "/help":
		return pkgchannel.WelcomeMessage, true
	case "/new":
		info, err := rc.ResolveSession()
		if err != nil {
			return fmt.Sprintf("Error: %v", err), true
		}
		if info.Source == "chat" {
			// Temp chat: archive it and return to the main session.
			if err := rc.Pool.ArchiveSession(info.ID); err != nil {
				return fmt.Sprintf("Error archiving session: %v", err), true
			}
			return "Session archived. Returning to main session.", true
		}
		// Main session: compact context instead of rotating.
		if _, err := rc.CompactSession(ctx); err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return "Context compacted.", true
	case "/compact":
		if _, err := rc.CompactSession(ctx); err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return "Session compacted.", true
	case "/temp":
		if _, err := rc.RotateSession(); err != nil {
			return fmt.Sprintf("Error creating temp session: %v", err), true
		}
		return "Temporary session started. Use /new to return to your main session.", true
	case "/whoami":
		return fmt.Sprintf("Your ID: %s", senderID), true
	}

	return "", false
}
