package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/chatroute"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// HandleCommand processes common bot commands shared across all channels.
// Returns the response text and whether the command was handled.
// /model and /agent are left to each channel (they need platform-specific UI).
func HandleCommand(ctx context.Context, rc *chatroute.ResolvedChat, text, senderID string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start", "/help":
		return pkgchannel.WelcomeMessage, true

	case "/new":
		info, err := rc.RotateSession()
		if err != nil {
			return fmt.Sprintf("Error creating new session: %v", err), true
		}
		_ = info
		return "New session started.", true

	case "/compact":
		_, err := rc.CompactSession(ctx)
		if err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return "Session compacted.", true

	case "/whoami":
		return fmt.Sprintf("Your ID: %s", senderID), true
	}

	return "", false
}
