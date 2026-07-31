package session

import (
	"context"

	"github.com/CherryHQ/stella/internal/memory"
)

// store is the private persistence interface used by Registry.
// The only production implementation is memoryAdapter, which delegates to
// memory.SessionManager. Tests may substitute a fake.
type store interface {
	save(ctx context.Context, info Info) error
	// rotate archives expectedSessionID and creates successor atomically,
	// reporting ErrStaleRotation when expectedSessionID is no longer active.
	rotate(ctx context.Context, expectedSessionID string, successor Info) error
	load(ctx context.Context, sessionID, userID, agentID string) (Info, error)
	list(ctx context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error)
	listForReview(ctx context.Context, agentID string, opts memory.ListOptions) ([]Info, error)
}
