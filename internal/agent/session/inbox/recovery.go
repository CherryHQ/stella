package inbox

import (
	"context"
	"errors"
	"fmt"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const recoveryPageSize = 100 // synchronous startup drain; page larger if profiling shows DB overhead

// ErrTargetUnavailable marks a permanent fresh-authorization failure. Recovery
// terminalizes that row and continues; infrastructure errors abort startup.
var ErrTargetUnavailable = errors.New("session inbox target unavailable")

// RecoveryResolver reauthorizes both durable Session endpoints and returns the
// exact target scope for append-only delivery.
type RecoveryResolver interface {
	ResolveInboxDelivery(ctx context.Context, sourceSessionID, targetSessionID, actorID string) (agentsession.Info, error)
}

// Recover drains pending rows in durable enqueue order. It only appends inputs;
// this package has no runtime/runner dependency and therefore cannot replay a
// model or tool turn.
func (s *Store) Recover(ctx context.Context, resolver RecoveryResolver, appender memory.InboxAppender) error {
	if s == nil || s.q == nil || resolver == nil || appender == nil {
		return errors.New("session inbox recovery is not configured")
	}
	// Associated work belongs to its AgentRun and is never replayed. Only rows
	// whose terminal Run can no longer deliver are converged here; linked running
	// rows remain observable for the Run reaper.
	if _, err := s.q.TerminalizeLinkedSessionInbox(ctx); err != nil {
		return fmt.Errorf("terminalize linked session inbox: %w", err)
	}
	var cursor int64
	for {
		rows, err := s.q.ListPendingSessionInbox(ctx, sqlc.ListPendingSessionInboxParams{
			AfterEnqueueSeq: cursor,
			PageSize:        recoveryPageSize,
		})
		if err != nil {
			return fmt.Errorf("list pending session inbox: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if !row.EnqueueSeq.Valid {
				return errors.New("pending session inbox row has no sequence")
			}
			cursor = row.EnqueueSeq.Int64
			target, err := resolver.ResolveInboxDelivery(ctx, row.SourceSessionID, row.TargetSessionID, row.ActorID)
			if errors.Is(err, ErrTargetUnavailable) {
				if _, failErr := s.FailPending(ctx, row.ID, ErrorTargetUnavailable); failErr != nil {
					return fmt.Errorf("terminalize unavailable session inbox %s: %w", row.ID, failErr)
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("authorize session inbox %s: %w", row.ID, err)
			}
			memSession, err := target.MemoryScope()
			if err != nil {
				return fmt.Errorf("target memory scope for session inbox %s: %w", row.ID, err)
			}
			deliveryCtx := authz.WithUserID(ctx, target.UserID)
			deliveryCtx = authz.WithAgentID(deliveryCtx, target.AgentID)
			deliveryCtx = eventlog.WithMessageActor(deliveryCtx, eventlog.MessageActor{
				Type: eventlog.ActorAgent, ID: row.ActorID, SourceSessionID: row.SourceSessionID,
			})
			err = appender.AppendInboxInput(deliveryCtx, memSession, row.ID, ai.UserMessage{Content: row.Content})
			if errors.Is(err, memory.ErrInboxNotPending) {
				continue
			}
			if err != nil {
				return fmt.Errorf("append recovered session inbox %s: %w", row.ID, err)
			}
		}
	}
}
