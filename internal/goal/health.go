package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// HealthFilter scopes the goal execution health report.
type HealthFilter struct {
	SinceAt time.Time
	UntilAt time.Time
	UserID  string
	AgentID string
}

// HealthReport is the aggregated execution health view consumed by the API/CLI.
type HealthReport struct {
	SinceAt              time.Time                  `json:"since_at"`
	UntilAt              time.Time                  `json:"until_at"`
	UserID               *string                    `json:"user_id,omitempty"`
	AgentID              *string                    `json:"agent_id,omitempty"`
	TotalGoals           int64                      `json:"total_goals"`
	LifecycleCounts      []HealthCountBucket        `json:"lifecycle_counts"`
	BlockedReasonCounts  []HealthCountBucket        `json:"blocked_reason_counts"`
	AttemptPurposes      []HealthAttemptPurpose     `json:"attempt_purposes"`
	FailureClassCounts   []HealthCountBucket        `json:"failure_class_counts"`
	AcceptanceEvents     HealthAcceptanceEvents     `json:"acceptance_events"`
	DecompositionQuality HealthDecompositionQuality `json:"decomposition_quality"`
	BudgetAttribution    HealthBudgetAttribution    `json:"budget_attribution"`
	Latency              HealthLatency              `json:"latency"`
}

type HealthCountBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type HealthRatioBucket struct {
	Key   string  `json:"key"`
	Count int64   `json:"count"`
	Ratio float64 `json:"ratio"`
}

type HealthAttemptPurpose struct {
	Purpose        string  `json:"purpose"`
	Total          int64   `json:"total"`
	Succeeded      int64   `json:"succeeded"`
	SuccessRate    float64 `json:"success_rate"`
	AverageRetries float64 `json:"average_retries"`
	MaxRetries     int64   `json:"max_retries"`
}

type HealthAcceptanceEvents struct {
	Total    int64   `json:"total"`
	Passed   int64   `json:"passed"`
	Failed   int64   `json:"failed"`
	PassRate float64 `json:"pass_rate"`
}

type HealthDecompositionQuality struct {
	FirstRoundTotal       int64               `json:"first_round_total"`
	FirstRoundSucceeded   int64               `json:"first_round_succeeded"`
	FirstRoundSuccessRate float64             `json:"first_round_success_rate"`
	AverageRepairRounds   float64             `json:"average_repair_rounds"`
	RedecompositionCounts []HealthCountBucket `json:"redecomposition_counts"`
}

type HealthBudgetAttribution struct {
	ModelBudgetAttempts       int64               `json:"model_budget_attempts"`
	ClassCounts               []HealthRatioBucket `json:"class_counts"`
	FlakyDominantBlockedGoals int64               `json:"flaky_dominant_blocked_goals"`
}

type HealthLatency struct {
	Attempts []HealthAttemptLatency `json:"attempts"`
	GoalE2E  HealthLatencyBucket    `json:"goal_e2e"`
}

type HealthAttemptLatency struct {
	Purpose string   `json:"purpose"`
	P50Ms   *float64 `json:"p50_ms"`
	P95Ms   *float64 `json:"p95_ms"`
}

type HealthLatencyBucket struct {
	P50Ms *float64 `json:"p50_ms"`
	P95Ms *float64 `json:"p95_ms"`
}

// healthWindow resolves the report's [since, until] window, applying the 14-day
// default so the policy pre-scan in Access.HealthReport and the aggregation below
// observe the exact same lower bound.
func (s *GoalService) healthWindow(filter HealthFilter) (since, until time.Time) {
	since = filter.SinceAt
	if since.IsZero() {
		since = s.nowTime().Add(-14 * 24 * time.Hour)
	}
	until = filter.UntilAt
	if until.IsZero() {
		until = s.nowTime()
	}
	return since.UTC(), until.UTC()
}

// HealthReport aggregates goal, attempt, and acceptance-event rows for one time
// window, restricted to goalIDs — the set the caller was authorized to read by the
// Access PEP. An empty set yields an empty report; the caller (Access.HealthReport)
// is the only supported entry so per-row policy is always applied first.
func (s *GoalService) HealthReport(ctx context.Context, filter HealthFilter, goalIDs []string) (HealthReport, error) {
	filter.SinceAt, filter.UntilAt = s.healthWindow(filter)

	if goalIDs == nil {
		goalIDs = []string{}
	}
	raw, err := s.q.GetGoalHealthReport(ctx, sqlc.GetGoalHealthReportParams{
		SinceAt: filter.SinceAt,
		UserID:  pgnull.Text(filter.UserID),
		AgentID: pgnull.Text(filter.AgentID),
		GoalIds: goalIDs,
	})
	if err != nil {
		return HealthReport{}, fmt.Errorf("goal health report: %w", err)
	}
	var out HealthReport
	if err := json.Unmarshal(raw, &out); err != nil {
		return HealthReport{}, fmt.Errorf("decode goal health report: %w", err)
	}
	out.SinceAt = filter.SinceAt
	out.UntilAt = filter.UntilAt
	if filter.UserID != "" {
		out.UserID = &filter.UserID
	}
	if filter.AgentID != "" {
		out.AgentID = &filter.AgentID
	}
	return out, nil
}
