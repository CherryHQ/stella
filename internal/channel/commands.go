package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// HandleCommand processes common bot commands shared across all channels.
// /model and /agent are left to each channel because they need platform-specific UI.
//
// /new is deliberately absent: rotating a session must run in the same
// per-session FIFO queue as chat turns, so the coordinator owns it (see
// Coordinator.handleNewSessionCommand) and calls NewSessionReply from there.
func HandleCommand(ctx context.Context, rc *ResolvedChat, text, senderID string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start", "/help":
		return pkgchannel.WelcomeMessage, true
	case "/compact":
		if _, err := rc.CompactSession(ctx); err != nil {
			if errors.Is(err, agent.ErrGroupCompactionUnsupported) {
				return pkgchannel.GroupCompactUnsupportedMessage, true
			}
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return "Session compacted.", true
	case "/whoami":
		return fmt.Sprintf("Your ID: %s", senderID), true
	}

	return "", false
}

// NewSessionReply rotates the chat onto a fresh session and returns the user
// reply. expectedSessionID is the session observed when the command arrived; a
// rotation that no longer matches it is reported as an already-done reset rather
// than resetting the successor a concurrent /new just created.
func NewSessionReply(ctx context.Context, rc *ResolvedChat, expectedSessionID string) string {
	switch _, err := rc.RotateSession(ctx, expectedSessionID); {
	case err == nil:
		return pkgchannel.NewSessionStartedMessage
	case errors.Is(err, session.ErrStaleRotation):
		return pkgchannel.SessionAlreadyResetMessage
	default:
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
}
