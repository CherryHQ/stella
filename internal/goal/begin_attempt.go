package goal

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type attemptSpec struct {
	purpose       string
	sessionID     string
	executorAgent string
	lease         pgtype.Timestamptz
	enqueue       AttemptEnqueuer
	prepare       func(context.Context, *sqlc.Queries, sqlc.AgentGoal, int) (AttemptInput, error)
	transition    func(context.Context, *sqlc.Queries, sqlc.AgentGoal, sqlc.AgentGoalAttempt) error
}

func (s *GoalService) beginAttempt(ctx context.Context, goalID string, spec attemptSpec) (sqlc.AgentGoalAttempt, error) {
	var out sqlc.AgentGoalAttempt
	err := s.withTxRaw(ctx, func(q *sqlc.Queries, tx pgx.Tx) error {
		if err := q.LockGoalForWrite(ctx, goalID); err != nil {
			return fmt.Errorf("lock goal for %s attempt: %w", spec.purpose, err)
		}
		d, err := getGoal(ctx, q, goalID)
		if err != nil {
			return err
		}
		maxNo, err := q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: d.ID, Purpose: spec.purpose})
		if err != nil {
			return fmt.Errorf("max %s attempt no: %w", spec.purpose, err)
		}
		attemptNo := int(maxNo) + 1
		input, err := spec.prepare(ctx, q, d, attemptNo)
		if err != nil {
			return err
		}
		att, err := q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
			ID:              newID(),
			GoalID:          d.ID,
			UserID:          d.UserID,
			AgentID:         pgnull.Text(d.AgentID),
			ExecutorAgentID: pgnull.Text(spec.executorAgent),
			SessionID:       spec.sessionID,
			Purpose:         spec.purpose,
			AttemptNo:       int64(attemptNo),
			Status:          AttemptQueued,
			InputContext:    marshalJSON(input),
			LeaseExpiresAt:  spec.lease,
		})
		if err != nil {
			return fmt.Errorf("create %s attempt: %w", spec.purpose, err)
		}
		if spec.transition != nil {
			if err := spec.transition(ctx, q, d, att); err != nil {
				return err
			}
		}
		if spec.enqueue != nil {
			if err := spec.enqueue(ctx, tx, d.ID, att.ID); err != nil {
				return fmt.Errorf("enqueue %s attempt: %w", spec.purpose, err)
			}
		}
		out = att
		return nil
	})
	return out, err
}
