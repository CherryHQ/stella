package goal

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHealthReportAggregatesGoalExecutionMetrics(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	accepted := h.createRoot(KindLeaf, AcceptanceContract{})
	blocked := h.createRoot(KindLeaf, AcceptanceContract{})
	cancelled := h.createRoot(KindLeaf, AcceptanceContract{})

	h.setGoalHealthState(accepted.ID, LifecycleDone, "", now.Add(-10*time.Hour), now.Add(-8*time.Hour))
	h.setGoalHealthState(blocked.ID, LifecycleBlocked, BlockEnvUnavailable, now.Add(-9*time.Hour), now.Add(-6*time.Hour))
	h.setGoalHealthState(cancelled.ID, LifecycleDone, "", now.Add(-5*time.Hour), now.Add(-4*time.Hour))

	acceptedExec2 := h.insertHealthAttempt(accepted.ID, PurposeExecution, 2, AttemptSubmitted, "", 0, now.Add(-8*time.Hour), now.Add(-8*time.Hour+2*time.Minute))
	h.insertHealthAttempt(accepted.ID, PurposeDecomposition, 1, AttemptSubmitted, "", 2, now.Add(-10*time.Hour), now.Add(-10*time.Hour+time.Minute))
	h.insertHealthAttempt(accepted.ID, PurposeExecution, 1, AttemptFailed, FailureClassFlaky, 0, now.Add(-9*time.Hour), now.Add(-9*time.Hour+time.Minute))

	h.insertHealthAttempt(blocked.ID, PurposeDecomposition, 1, AttemptFailed, FailureClassModel, 2, now.Add(-9*time.Hour), now.Add(-9*time.Hour+time.Minute))
	h.insertHealthAttempt(blocked.ID, PurposeDecomposition, 2, AttemptSubmitted, "", 0, now.Add(-8*time.Hour), now.Add(-8*time.Hour+time.Minute))
	h.insertHealthAttempt(blocked.ID, PurposeExecution, 1, AttemptFailed, FailureClassFlaky, 0, now.Add(-7*time.Hour), now.Add(-7*time.Hour+time.Minute))
	h.insertHealthAttempt(blocked.ID, PurposeExecution, 2, AttemptFailed, FailureClassModel, 0, now.Add(-7*time.Hour), now.Add(-7*time.Hour+2*time.Minute))
	h.insertHealthAttempt(blocked.ID, PurposeExecution, 3, AttemptInterrupted, FailureClassFlaky, 0, now.Add(-7*time.Hour), now.Add(-7*time.Hour+3*time.Minute))

	h.insertAcceptanceEvent(accepted.ID, acceptedExec2, 0, "pass")
	h.insertAcceptanceEvent(accepted.ID, acceptedExec2, 1, "fail")

	// Route through the Access PEP so the aggregation runs over authorized rows.
	report, err := h.begin(t, h.userAuth(t, h.userID)).HealthReport(ctx, HealthFilter{SinceAt: now.Add(-14 * 24 * time.Hour), UntilAt: now, UserID: h.userID})
	if err != nil {
		t.Fatalf("HealthReport: %v", err)
	}
	if report.TotalGoals != 3 {
		t.Fatalf("total_goals=%d want 3", report.TotalGoals)
	}
	assertCount(t, report.LifecycleCounts, LifecycleDone, 2)
	assertCount(t, report.LifecycleCounts, LifecycleBlocked, 1)
	assertCount(t, report.BlockedReasonCounts, BlockEnvUnavailable, 1)

	exec := findPurpose(t, report.AttemptPurposes, PurposeExecution)
	if exec.Total != 5 || exec.Succeeded != 1 || exec.MaxRetries != 2 || !near(exec.AverageRetries, 1.5) {
		t.Fatalf("execution purpose=%+v want total=5 succeeded=1 avg_retries=1.5 max_retries=2", exec)
	}
	assertCount(t, report.FailureClassCounts, FailureClassFlaky, 3)
	assertCount(t, report.FailureClassCounts, FailureClassModel, 2)

	if report.AcceptanceEvents.Total != 2 || report.AcceptanceEvents.Passed != 1 || report.AcceptanceEvents.Failed != 1 || !near(report.AcceptanceEvents.PassRate, 0.5) {
		t.Fatalf("acceptance_events=%+v want 1/2 pass", report.AcceptanceEvents)
	}
	dq := report.DecompositionQuality
	if dq.FirstRoundTotal != 2 || dq.FirstRoundSucceeded != 1 || !near(dq.FirstRoundSuccessRate, 0.5) || !near(dq.AverageRepairRounds, 4.0/3.0) {
		t.Fatalf("decomposition_quality=%+v", dq)
	}
	assertCount(t, dq.RedecompositionCounts, "0", 2)
	assertCount(t, dq.RedecompositionCounts, "1", 1)

	ba := report.BudgetAttribution
	if ba.ModelBudgetAttempts != 1 || ba.FlakyDominantBlockedGoals != 1 {
		t.Fatalf("budget_attribution=%+v want model_budget=1 flaky_dominant=1", ba)
	}
	flaky := findRatio(t, ba.ClassCounts, FailureClassFlaky)
	if flaky.Count != 3 || !near(flaky.Ratio, 0.75) {
		t.Fatalf("flaky budget ratio=%+v want 3 / 0.75", flaky)
	}
	if report.Latency.GoalE2E.P50Ms == nil || *report.Latency.GoalE2E.P50Ms <= 0 {
		t.Fatalf("goal e2e p50 missing: %+v", report.Latency.GoalE2E)
	}
}

func (h *harness) setGoalHealthState(id, lifecycle, blockReason string, createdAt, endedAt time.Time) {
	h.t.Helper()
	acceptedOutput := any(nil)
	acceptedAt := any(nil)
	cancelledAt := any(nil)
	doneReason := ""
	acceptanceState := AcceptancePending
	if lifecycle == LifecycleDone {
		acceptedOutput = `{"summary":"ok"}`
		acceptedAt = endedAt
		acceptanceState = AcceptancePassed
		doneReason = DoneReasonAccepted
	}
	if _, err := h.db.Exec(context.Background(), `
		UPDATE agent_goal
		SET lifecycle = $2,
		    block_reason = $3,
		    done_reason = $4,
		    acceptance_state = $5,
		    accepted_output = $6,
		    accepted_at = $7,
		    cancelled_at = $8,
		    created_at = $9,
		    updated_at = $10
		WHERE id = $1`, id, lifecycle, blockReason, doneReason, acceptanceState, acceptedOutput, acceptedAt, cancelledAt, createdAt, endedAt); err != nil {
		h.t.Fatalf("set goal state: %v", err)
	}
}

func (h *harness) insertHealthAttempt(goalID, purpose string, attemptNo int, status, failureClass string, repairRounds int, startedAt, finishedAt time.Time) string {
	h.t.Helper()
	id := uuid.NewString()
	sid, err := h.sessionMinter()(context.Background(), h.userID, h.agentID, "")
	if err != nil {
		h.t.Fatalf("mint attempt session: %v", err)
	}
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO agent_goal_attempt (
			id, goal_id, user_id, agent_id, session_id, purpose, attempt_no,
			status, started_at, finished_at, failure_class, repair_rounds, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		id, goalID, h.userID, h.agentID, sid, purpose, attemptNo, status, startedAt, finishedAt, failureClass, repairRounds, startedAt, finishedAt); err != nil {
		h.t.Fatalf("insert attempt: %v", err)
	}
	return id
}

func (h *harness) insertAcceptanceEvent(goalID, attemptID string, seq int, result string) {
	h.t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO agent_goal_acceptance_event (
			id, goal_id, attempt_id, seq, item_id, item_kind, result, exit_code, cache_key
		) VALUES ($1, $2, $3, $4, $5, 'deterministic', $6, 0, $7)`,
		uuid.NewString(), goalID, attemptID, seq, "check-"+result, result, "cache-"+result); err != nil {
		h.t.Fatalf("insert acceptance event: %v", err)
	}
}

func assertCount(t *testing.T, rows []HealthCountBucket, key string, want int64) {
	t.Helper()
	for _, row := range rows {
		if row.Key == key {
			if row.Count != want {
				t.Fatalf("count[%s]=%d want %d", key, row.Count, want)
			}
			return
		}
	}
	t.Fatalf("count[%s] missing in %+v", key, rows)
}

func findPurpose(t *testing.T, rows []HealthAttemptPurpose, purpose string) HealthAttemptPurpose {
	t.Helper()
	for _, row := range rows {
		if row.Purpose == purpose {
			return row
		}
	}
	t.Fatalf("purpose %s missing in %+v", purpose, rows)
	return HealthAttemptPurpose{}
}

func findRatio(t *testing.T, rows []HealthRatioBucket, key string) HealthRatioBucket {
	t.Helper()
	for _, row := range rows {
		if row.Key == key {
			return row
		}
	}
	t.Fatalf("ratio %s missing in %+v", key, rows)
	return HealthRatioBucket{}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 0.0001
}
