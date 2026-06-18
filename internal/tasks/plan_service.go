package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// plan_service.go is the accepted-plan gate's write path (#525). It owns the
// plan row's lifecycle (write pending edit -> accept -> materialize) and the
// materializer that turns an accepted plan into work tasks. Work tasks exist
// only through here: the public CreateTask refuses a goal_id, so a goal can
// never reach 'running' with hand-attached children.
//
// Phase 2 lands the direct single-item path end to end. The generic multi-item
// materializer (deps, idempotent re-materialize, replan reconciliation) extends
// MaterializeGoalPlan in Phase 3.

// directPlanItemID is the stable item id of a direct plan's single item, so a
// re-materialize maps back to the same task rather than spawning a duplicate.
const directPlanItemID = "main"

// CreateAndAcceptDirectPlan writes a goal's direct plan, accepts it (review
// policy none), and materializes it — yielding exactly one work task — leaving
// the goal in 'planned'. This is the gate's happy path for a direct goal:
// afterwards ActivateGoal can promote planned -> running with a real ready
// child, so there is no empty-running window (codex BLOCKER 1).
func (f *ServiceFacade) CreateAndAcceptDirectPlan(ctx context.Context, goal sqlc.AgentGoal) error {
	raw, err := buildDirectPlanContent(goal)
	if err != nil {
		return err
	}
	sessions, err := f.mintDirectPlanSession(ctx, goal)
	if err != nil {
		return err
	}
	now := f.svc.now()
	err = f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		return f.createAndAcceptDirectPlanInTx(ctx, q, goal, raw, sessions, now)
	})
	if err != nil {
		return fmt.Errorf("CreateAndAcceptDirectPlan: %w", err)
	}
	return nil
}

// mintDirectPlanSession pre-mints the single work-task session for a direct
// plan, keyed by item id. Sessions are minted before the tx: SQLite is
// single-writer, so a mint (separate connection) inside the open write tx
// self-deadlocks. A rolled-back materialize orphans at most this session row,
// which the idempotent re-Get on rerun reuses (Phase 3 SF1).
func (f *ServiceFacade) mintDirectPlanSession(ctx context.Context, goal sqlc.AgentGoal) (map[string]string, error) {
	if f.newSession == nil {
		return nil, fmt.Errorf("%w: task session minter is not configured", ErrInvalidTaskContext)
	}
	sessionID, err := f.newSession(ctx, goal.UserID, goal.AgentID, goal.ProjectID.String)
	if err != nil {
		return nil, fmt.Errorf("mint plan-task session: %w", err)
	}
	return map[string]string{directPlanItemID: sessionID}, nil
}

// createAndAcceptDirectPlanInTx is the tx body shared by CreateAndAcceptDirectPlan
// and CreateGoal's direct path, so the latter can insert goal + plan + task in one
// transaction (no ghost draft goal left behind if materialize fails). The session
// is pre-minted by the caller outside the tx.
func (f *ServiceFacade) createAndAcceptDirectPlanInTx(ctx context.Context, q *sqlc.Queries, goal sqlc.AgentGoal, rawContent string, sessions map[string]string, now string) error {
	// Upsert-pending then accept. The goal has no plan row yet for the create
	// path, but the upsert also covers a later direct re-plan (goal_id is UNIQUE,
	// so this is never a blind 2nd insert).
	if err := q.UpsertAgentGoalPlanPending(ctx, sqlc.UpsertAgentGoalPlanPendingParams{
		ID:                 uuid.NewString(),
		GoalID:             goal.ID,
		Status:             PlanStatusAccepted,
		ReviewPolicy:       ReviewPolicyNone,
		PendingContentJson: nullable(rawContent),
	}); err != nil {
		return fmt.Errorf("upsert direct plan: %w", err)
	}
	plan, err := q.GetAgentGoalPlanByGoal(ctx, goal.ID)
	if err != nil {
		return fmt.Errorf("load direct plan: %w", err)
	}
	if err := q.SetAgentGoalPlanAccepted(ctx, sqlc.SetAgentGoalPlanAcceptedParams{
		Status:     PlanStatusAccepted,
		AcceptedAt: nullable(now),
		ID:         plan.ID,
	}); err != nil {
		return fmt.Errorf("accept direct plan: %w", err)
	}
	return f.materializeGoalPlanInTx(ctx, q, goal, plan, sessions, now)
}

// buildDirectPlanContent returns the marshaled single-item direct plan content
// for a goal, validated in direct mode.
func buildDirectPlanContent(goal sqlc.AgentGoal) (string, error) {
	content := PlanContent{Items: []PlanItem{{
		ID:    directPlanItemID,
		Title: goal.Title,
		Role:  PlanRoleDirect,
	}}}
	if err := validatePlan(content); err != nil {
		return "", err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("marshal direct plan: %w", err)
	}
	return string(raw), nil
}

// materializeGoalPlanInTx promotes the plan's pending edit to materialized
// content, creates the plan's work tasks idempotently, stamps materialized_at,
// and conditionally promotes the goal to 'planned'. All of it runs in the
// caller's tx so a partial materialize never escapes (D1/2nd-pass B1).
//
// Phase 2 handles the direct single-item plan. Phase 3 generalizes the task
// loop to deps + replan reconciliation.
func (f *ServiceFacade) materializeGoalPlanInTx(ctx context.Context, q *sqlc.Queries, goal sqlc.AgentGoal, plan sqlc.AgentGoalPlan, sessions map[string]string, now string) error {
	if plan.Status != PlanStatusAccepted && plan.Status != PlanStatusApproved {
		return fmt.Errorf("%w: plan status %q is not accepted", ErrAcceptedPlanRequired, plan.Status)
	}
	if isTerminalGoalStatus(goal.Status) {
		return fmt.Errorf("%w: goal is %s", ErrInvalidTransition, goal.Status)
	}
	// The edit under materialization is the pending draft when present, else the
	// already-materialized content (a re-materialize with no staged change).
	rawContent := plan.ContentJson
	if plan.PendingContentJson.Valid {
		rawContent = plan.PendingContentJson.String
	}
	content, err := parsePlanContent(rawContent)
	if err != nil {
		return err
	}
	if err := validatePlan(content); err != nil {
		return err
	}
	isDirect := len(content.Items) == 1 &&
		(content.Items[0].Role == "" || content.Items[0].Role == PlanRoleDirect)

	existing, err := q.ListChildrenByGoal(ctx, sql.NullString{String: goal.ID, Valid: true})
	if err != nil {
		return fmt.Errorf("list goal children: %w", err)
	}
	byItem := make(map[string]sqlc.AgentTask, len(existing))
	for _, t := range existing {
		if t.SourcePlanID.Valid && t.SourcePlanID.String == plan.ID && t.PlanItemID != "" {
			byItem[t.PlanItemID] = t
		}
	}

	for _, item := range content.Items {
		if _, ok := byItem[item.ID]; ok {
			continue // idempotent: a re-materialize keeps the existing task.
		}
		// Sessions are pre-minted outside the tx (SQLite single-writer; Phase 3
		// SF1) and handed in keyed by item id.
		sessionID, ok := sessions[item.ID]
		if !ok {
			return fmt.Errorf("materialize: no session minted for plan item %q", item.ID)
		}
		// A direct plan's single task mirrors the goal's description; structured
		// items carry their own working context via the Phase 4 packet instead.
		description := ""
		if isDirect {
			description = goal.Description
		}
		if err := f.createPlanTaskInTx(ctx, q, goal, plan, item, description, sessionID, now); err != nil {
			return err
		}
	}

	if err := q.PromoteAgentGoalPlanMaterialized(ctx, sqlc.PromoteAgentGoalPlanMaterializedParams{
		MaterializedAt: nullable(now),
		ID:             plan.ID,
	}); err != nil {
		return fmt.Errorf("promote materialized: %w", err)
	}
	// Promote the goal to 'planned' only from a pre-run state, so a replan
	// re-materialize on a running goal is a no-op, not a regression (opus B2).
	if _, err := q.PromoteAgentGoalToPlanned(ctx, sqlc.PromoteAgentGoalToPlannedParams{
		UpdatedAt: now,
		ID:        goal.ID,
	}); err != nil {
		return fmt.Errorf("promote goal planned: %w", err)
	}
	return f.svc.appendGoalEvent(ctx, q, goal.ID, "goal_plan_materialized", goal.Status, GoalStatusPlanned, SystemActor(),
		map[string]any{"plan_id": plan.ID, "direct": isDirect})
}

// createPlanTaskInTx creates one work task from a plan item in 'draft', carrying
// the (source_plan_id, plan_item_id) traceability the materializer reconciles
// on. ActivateGoal flips these draft children to ready.
func (f *ServiceFacade) createPlanTaskInTx(ctx context.Context, q *sqlc.Queries, goal sqlc.AgentGoal, plan sqlc.AgentGoalPlan, item PlanItem, description, sessionID, now string) error {
	_, err := q.CreateAgentPlanTask(ctx, sqlc.CreateAgentPlanTaskParams{
		ID:           uuid.NewString(),
		UserID:       goal.UserID,
		AgentID:      goal.AgentID,
		SessionID:    sessionID,
		GoalID:       nullable(goal.ID),
		ProjectID:    goal.ProjectID,
		SourcePlanID: nullable(plan.ID),
		PlanItemID:   item.ID,
		Title:        item.Title,
		Description:  description,
		Status:       StatusDraft,
		Priority:     goal.Priority,
		Required:     1,
		RetryCount:   0,
		MaxRetries:   3,
		Context:      "{}",
		Output:       "{}",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		return fmt.Errorf("create plan task %q: %w", item.ID, err)
	}
	return nil
}
