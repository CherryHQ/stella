// Package session owns agent-session lifecycle.
//
// It is the sole authority over session creation, resumption, kind/channel
// policy, main-session resolution, review candidate selection, and the
// conversion from a validated session record to a memory operation scope.
//
// Production code outside this package and runtime should obtain memory.Session
// via Registry.MemoryScope. The runtime is allowed to construct it directly
// from validated Info because its inputs always come through the Registry.
package session

import (
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

// Kind is the typed session kind.
type Kind string

const (
	KindMain      Kind = "main"
	KindChat      Kind = "chat"
	KindDelegate  Kind = "delegate"
	KindTask      Kind = "task"
	KindScheduler Kind = "scheduler"
)

// Channel is the typed originating channel.
type Channel string

const (
	ChannelWeb       Channel = "web"
	ChannelCLI       Channel = "cli"
	ChannelTelegram  Channel = "telegram"
	ChannelDelegate  Channel = "delegate"
	ChannelTask      Channel = "task"
	ChannelScheduler Channel = "scheduler"
	ChannelWebhook   Channel = "webhook"
)

// Info holds metadata about an agent session.
// It wraps memory.SessionInfo so callers interact with a session-domain type
// while the underlying storage stays in the memory layer.
type Info = memory.SessionInfo

// NewInfo constructs a fresh Info with the required fields set.
func NewInfo(id, agentID, userID, channel string, kind Kind, projectID string, now time.Time) Info {
	return Info{
		ID:         id,
		AgentID:    agentID,
		UserID:     userID,
		Channel:    channel,
		Kind:       string(kind),
		ProjectID:  projectID,
		CreatedAt:  now,
		LastActive: now,
	}
}
