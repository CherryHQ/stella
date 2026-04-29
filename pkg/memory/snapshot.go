package memory

import (
	"context"
	"time"
)

// VersionedProfileStore is implemented by providers that can read profile/soul
// at a specific memory version (from the changelog).
// Version 0 or negative means "current" (same as ProfileStore).
type VersionedProfileStore interface {
	GetProfileAt(ctx context.Context, userID int64, agentID string, version int64) (string, error)
	GetAgentSoulAt(ctx context.Context, userID int64, agentID string, version int64) (string, error)
}

// VersionedConstraintStore is implemented by providers that can read constraints
// at a specific memory version.
type VersionedConstraintStore interface {
	GetConstraintsAt(ctx context.Context, userID int64, agentID string, version int64) ([]ConstraintEntry, error)
}

// SessionSnapshot holds the frozen version for a session.
type SessionSnapshot struct {
	SessionID string
	UserID    int64
	AgentID   string
	Version   int64
	// UpdatedAt is the wall-clock time when this snapshot was last advanced.
	// Used to filter knowledge entries so that knowledge activated after the
	// snapshot was taken does not appear in the frozen session.
	UpdatedAt time.Time
}

// SessionSnapshotStore is implemented by providers that support session snapshots.
type SessionSnapshotStore interface {
	// GetOrCreateSessionSnapshot returns the snapshot for the session, creating it
	// if it doesn't exist. When creating, freezes the current ctx_agent_memory.version.
	GetOrCreateSessionSnapshot(ctx context.Context, sessionID string, userID int64, agentID string) (SessionSnapshot, error)

	// AdvanceSessionSnapshot updates the snapshot version to the current
	// ctx_agent_memory.version for (userID, agentID).
	// Called after front-end memory writes so the current session sees the update.
	AdvanceSessionSnapshot(ctx context.Context, sessionID string, userID int64, agentID string) error
}
