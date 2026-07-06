package goal

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func effectiveAttemptBudget(d sqlc.AgentGoal) int {
	var pol ConvergencePolicy
	_ = unmarshalJSON(d.ConvergencePolicy, &pol)
	return pol.Normalized().MaxAttempts + int(d.BudgetBonus)
}

func (s *GoalService) spentAttemptBudget(ctx context.Context, q *sqlc.Queries, goalID, purpose string) (int, error) {
	spent, err := q.CountBillableAttempts(ctx, sqlc.CountBillableAttemptsParams{GoalID: goalID, Purpose: purpose})
	if err != nil {
		return 0, fmt.Errorf("count billable %s attempts: %w", purpose, err)
	}
	return int(spent), nil
}

func (s *GoalService) budgetLeft(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, purpose string) (bool, error) {
	spent, err := s.spentAttemptBudget(ctx, q, d.ID, purpose)
	if err != nil {
		return false, err
	}
	return spent < effectiveAttemptBudget(d), nil
}
