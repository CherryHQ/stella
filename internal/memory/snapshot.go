package memory

import (
	"context"
	"time"
)

// VersionedProfileStore is implemented by providers that can read profile/soul
// at a specific memory version (from the changelog).
// Every non-negative version is a frozen memory clock. Callers that need
// current state must use ProfileStore; negative versions return an error.
type VersionedProfileStore interface {
	GetProfileAt(ctx context.Context, userID string, agentID string, version int64) (string, error)
	GetAgentSoulAt(ctx context.Context, userID string, agentID string, version int64) (string, error)
}

// VersionedConstraintStore is implemented by providers that can read constraints
// at a non-negative frozen memory version. Callers that need current state must
// use ConstraintStore; negative versions return an error.
type VersionedConstraintStore interface {
	GetConstraintsAt(ctx context.Context, userID string, agentID string, version int64) ([]ConstraintEntry, error)
}

// SessionSnapshot holds the frozen version for a session.
type SessionSnapshot struct {
	SessionID string
	UserID    string
	AgentID   string
	Version   int64
	// UpdatedAt is the wall-clock time when this snapshot was last advanced.
	// It remains for legacy skill-backed knowledge filtering; v1 facts use
	// Version as their snapshot clock.
	UpdatedAt time.Time
}

// SessionSnapshotStore is implemented by providers that support session snapshots.
type SessionSnapshotStore interface {
	// GetOrCreateSessionSnapshot returns the snapshot for the session, creating it
	// if it doesn't exist. When creating, freezes the current ctx_agent_memory.version.
	GetOrCreateSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) (SessionSnapshot, error)

	// AdvanceSessionSnapshot updates the snapshot version to the current
	// ctx_agent_memory.version for (userID, agentID).
	// Called after front-end memory writes so the current session sees the update.
	AdvanceSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) error
}
