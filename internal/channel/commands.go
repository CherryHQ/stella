package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// HandleCommand processes common bot commands shared across all channels.
//
// /new is deliberately absent: rotating a session must run in the same
// per-session FIFO queue as chat turns, so the coordinator owns it (see
// Coordinator.handleNewSessionCommand → rotateChatSession).
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
