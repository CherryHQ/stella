package runtime

import (
	"context"

	"github.com/CherryHQ/stella/internal/memory"
)

// GroupResultCommitter accepts a group's output and transcript before the run
// completes. Runtime calls both methods serially; Commit must not wait for EOF.
// Platform publication belongs to the caller after the run has completed.
type GroupResultCommitter interface {
	Observe(Event) error
	Commit(context.Context, memory.DeferredGroupTurn) error
}

type groupResultKey struct{}

func WithGroupResultCommitter(ctx context.Context, committer GroupResultCommitter) context.Context {
	return context.WithValue(ctx, groupResultKey{}, committer)
}

func GroupResultCommitterFrom(ctx context.Context) (GroupResultCommitter, bool) {
	committer, ok := ctx.Value(groupResultKey{}).(GroupResultCommitter)
	return committer, ok && committer != nil
}
