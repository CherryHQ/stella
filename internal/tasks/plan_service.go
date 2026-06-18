package tasks

import (
	"context"
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

// materializeGoalPlanInTx is the single reconcile entry point — first materialize
// and replan run the same code (D7a). It promotes the plan's pending edit to
// materialized content, reconciles the work-task tree against the plan
// idempotently (keyed on (source_plan_id, plan_item_id), D7), wires deps, stamps
// materialized_at, and conditionally promotes the goal to 'planned'. All of it
// runs in the caller's tx so a partial materialize never escapes (D1/2nd-pass B1).
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

	// Existing tasks ever materialized from this plan (including detached ones),
	// keyed by item id; the basis for the reconcile diff.
	existing, err := q.ListAgentTaskBySourcePlan(ctx, nullable(plan.ID))
	if err != nil {
		return fmt.Errorf("list plan tasks: %w", err)
	}
	byItem := make(map[string]sqlc.AgentTask, len(existing))
	for _, t := range existing {
		if t.PlanItemID != "" {
			byItem[t.PlanItemID] = t
		}
	}
	desired := make(map[string]bool, len(content.Items))
	for _, it := range content.Items {
		desired[it.ID] = true
	}
	descFor := func(item PlanItem) string {
		// A direct plan's single task mirrors the goal's description; structured
		// items carry their own working context via the Phase 4 packet instead, so
		// their task description stays empty.
		if isDirect {
			return goal.Description
		}
		return ""
	}

	// Pre-check (D7a constrained replan): abort before any write if an edit would
	// change the definition of an item whose task is already in flight.
	for _, item := range content.Items {
		t, ok := byItem[item.ID]
		if !ok || isPlannableTask(t) {
			continue
		}
		changed, err := f.planItemDefinitionChanged(ctx, q, t, item, descFor(item), byItem)
		if err != nil {
			return err
		}
		if changed {
			return fmt.Errorf("%w: item %q is %s", ErrPlanItemInFlight, item.ID, t.Status)
		}
	}

	// Reconcile task existence. itemTaskID maps every in-plan item to its task id
	// (existing or freshly created) so the dep pass can wire edges.
	itemTaskID := make(map[string]string, len(content.Items))
	for _, item := range content.Items {
		t, ok := byItem[item.ID]
		if !ok {
			sessionID, ok := sessions[item.ID]
			if !ok {
				return fmt.Errorf("materialize: no session minted for plan item %q", item.ID)
			}
			id, err := f.createPlanTaskInTx(ctx, q, goal, plan, item, descFor(item), sessionID, now)
			if err != nil {
				return err
			}
			if err := f.writeCriteria(ctx, q, id, item.Criteria, now); err != nil {
				return err
			}
			itemTaskID[item.ID] = id
			continue
		}
		itemTaskID[item.ID] = t.ID
		if !isPlannableTask(t) {
			continue // in-flight & unchanged (pre-check passed); leave it be.
		}
		// Idempotency (D7): an unchanged item touches nothing — no metadata write,
		// no criteria churn, no claim-race window. The dep pass below is a no-op
		// for it too (want == have). Only a genuine edit takes the update path.
		changed, err := f.planItemDefinitionChanged(ctx, q, t, item, descFor(item), byItem)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		n, err := q.UpdateAgentTaskMetaIfPlannable(ctx, sqlc.UpdateAgentTaskMetaIfPlannableParams{
			Title:       item.Title,
			Description: descFor(item),
			UpdatedAt:   now,
			ID:          t.ID,
		})
		if err != nil {
			return fmt.Errorf("update plan task %q: %w", item.ID, err)
		}
		if n == 0 {
			// Raced to running between the read above and this write.
			return fmt.Errorf("%w: item %q raced to running", ErrPlanItemInFlight, item.ID)
		}
		if err := q.DeleteAgentTaskCriteriaByTask(ctx, t.ID); err != nil {
			return fmt.Errorf("clear criteria %q: %w", item.ID, err)
		}
		if err := f.writeCriteria(ctx, q, t.ID, item.Criteria, now); err != nil {
			return err
		}
	}

	// Removed items: cancel the not-started ones, detach those with output.
	for _, t := range existing {
		if t.PlanItemID == "" || desired[t.PlanItemID] || t.DetachedAt.Valid {
			continue
		}
		if isPlannableTask(t) {
			if err := f.cancelReconciledTask(ctx, q, goal.ID, t, now); err != nil {
				return err
			}
			continue
		}
		if t.Status == StatusCancelled {
			continue // already gone
		}
		if err := q.SetAgentTaskDetached(ctx, sqlc.SetAgentTaskDetachedParams{
			DetachedAt: nullable(now), UpdatedAt: now, ID: t.ID,
		}); err != nil {
			return fmt.Errorf("detach task %q: %w", t.PlanItemID, err)
		}
		if err := f.svc.appendGoalEvent(ctx, q, goal.ID, "goal_plan_task_detached", t.Status, t.Status, SystemActor(),
			map[string]any{"task_id": t.ID, "plan_item_id": t.PlanItemID}); err != nil {
			return err
		}
	}

	// Dep pass: every task id is now known, so wire each in-plan item's edges and
	// delete the ones the new plan dropped (stale-dep cleanup, 2nd-pass SF1).
	for _, item := range content.Items {
		t, matched := byItem[item.ID]
		if matched && !isPlannableTask(t) {
			continue // in-flight & unchanged; don't touch its resolved deps.
		}
		if err := f.reconcileTaskDeps(ctx, q, itemTaskID[item.ID], item, itemTaskID); err != nil {
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
	// Audit the real transition: only a draft/planning goal actually moved to
	// planned; a replan on a running goal stays running, so don't log a phantom
	// demote (codex audit nit).
	toStatus := goal.Status
	if goal.Status == GoalStatusDraft || goal.Status == GoalStatusPlanning {
		toStatus = GoalStatusPlanned
	}
	return f.svc.appendGoalEvent(ctx, q, goal.ID, "goal_plan_materialized", goal.Status, toStatus, SystemActor(),
		map[string]any{"plan_id": plan.ID, "direct": isDirect})
}

// isPlannableTask reports whether a task is still editable by reconcile: not
// started (draft/ready) and not claimed by a run. Anything else is in flight.
func isPlannableTask(t sqlc.AgentTask) bool {
	return (t.Status == StatusDraft || t.Status == StatusReady) && !t.ActiveRunID.Valid
}

// planItemDefinitionChanged reports whether a plan item's materialized definition
// (title, description, criteria, deps) differs from its existing task — the test
// that rejects editing an in-flight item (D7a).
func (f *ServiceFacade) planItemDefinitionChanged(ctx context.Context, q *sqlc.Queries, t sqlc.AgentTask, item PlanItem, wantDesc string, byItem map[string]sqlc.AgentTask) (bool, error) {
	if t.Title != item.Title || t.Description != wantDesc {
		return true, nil
	}
	criteria, err := q.ListAgentTaskCriteria(ctx, t.ID)
	if err != nil {
		return false, fmt.Errorf("list criteria %q: %w", item.ID, err)
	}
	if len(criteria) != len(item.Criteria) {
		return true, nil
	}
	for i, c := range criteria {
		if c.Description != item.Criteria[i] {
			return true, nil
		}
	}
	deps, err := q.ListAgentTaskDeps(ctx, t.ID)
	if err != nil {
		return false, fmt.Errorf("list deps %q: %w", item.ID, err)
	}
	have := make(map[string]bool, len(deps))
	for _, d := range deps {
		have[d.DepTaskID] = true
	}
	want := make(map[string]bool, len(item.Deps))
	for _, depItemID := range item.Deps {
		dep, ok := byItem[depItemID]
		if !ok {
			return true, nil // depends on an item with no materialized task yet
		}
		want[dep.ID] = true
	}
	if len(have) != len(want) {
		return true, nil
	}
	for id := range want {
		if !have[id] {
			return true, nil
		}
	}
	return false, nil
}

// reconcileTaskDeps makes the task's outgoing dep edges match the plan item:
// adds missing ones (validated, cycle-checked via addDepInTx) and deletes the
// edges the new plan dropped, including edges to removed/detached items.
func (f *ServiceFacade) reconcileTaskDeps(ctx context.Context, q *sqlc.Queries, taskID string, item PlanItem, itemTaskID map[string]string) error {
	want := make(map[string]bool, len(item.Deps))
	for _, depItemID := range item.Deps {
		depTaskID, ok := itemTaskID[depItemID]
		if !ok {
			return fmt.Errorf("reconcile deps: item %q depends on unmapped item %q", item.ID, depItemID)
		}
		want[depTaskID] = true
	}
	current, err := q.ListAgentTaskDeps(ctx, taskID)
	if err != nil {
		return fmt.Errorf("list deps for %q: %w", item.ID, err)
	}
	have := make(map[string]bool, len(current))
	for _, d := range current {
		have[d.DepTaskID] = true
		if !want[d.DepTaskID] {
			if err := q.DeleteAgentTaskDep(ctx, sqlc.DeleteAgentTaskDepParams{TaskID: taskID, DepTaskID: d.DepTaskID}); err != nil {
				return fmt.Errorf("delete stale dep %q->%q: %w", item.ID, d.DepTaskID, err)
			}
		}
	}
	for depTaskID := range want {
		if have[depTaskID] {
			continue
		}
		if err := f.svc.addDepInTx(ctx, q, taskID, depTaskID, DepKindHard, OnFailureBlock); err != nil {
			return fmt.Errorf("add dep %q->%q: %w", item.ID, depTaskID, err)
		}
	}
	return nil
}

// writeCriteria inserts a task's acceptance-criteria rows in plan order.
func (f *ServiceFacade) writeCriteria(ctx context.Context, q *sqlc.Queries, taskID string, criteria []string, now string) error {
	for i, desc := range criteria {
		if _, err := q.CreateAgentTaskCriterion(ctx, sqlc.CreateAgentTaskCriterionParams{
			ID:           uuid.NewString(),
			TaskID:       taskID,
			Description:  desc,
			RequiredFlag: 1,
			Position:     int64(i),
			CreatedAt:    now,
		}); err != nil {
			return fmt.Errorf("create criterion: %w", err)
		}
	}
	return nil
}

// cancelReconciledTask cancels a not-started task whose plan item the replan
// removed, clearing any active pointers first so the DB invariants hold.
func (f *ServiceFacade) cancelReconciledTask(ctx context.Context, q *sqlc.Queries, goalID string, t sqlc.AgentTask, now string) error {
	n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
		Status: StatusCancelled, UpdatedAt: now, ID: t.ID, Status_2: t.Status,
	})
	if err != nil {
		return fmt.Errorf("cancel removed task %q: %w", t.PlanItemID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: item %q raced during removal", ErrPlanItemInFlight, t.PlanItemID)
	}
	if err := q.SetAgentTaskCancelled(ctx, sqlc.SetAgentTaskCancelledParams{
		CancelledAt: nullable(now), UpdatedAt: now, ID: t.ID,
	}); err != nil {
		return fmt.Errorf("stamp cancelled %q: %w", t.PlanItemID, err)
	}
	return f.svc.appendGoalEvent(ctx, q, goalID, "goal_plan_task_removed", t.Status, StatusCancelled, SystemActor(),
		map[string]any{"task_id": t.ID, "plan_item_id": t.PlanItemID})
}

// createPlanTaskInTx creates one work task from a plan item in 'draft', carrying
// the (source_plan_id, plan_item_id) traceability the materializer reconciles
// on, and returns its id. ActivateGoal flips these draft children to ready.
func (f *ServiceFacade) createPlanTaskInTx(ctx context.Context, q *sqlc.Queries, goal sqlc.AgentGoal, plan sqlc.AgentGoalPlan, item PlanItem, description, sessionID, now string) (string, error) {
	id := uuid.NewString()
	_, err := q.CreateAgentPlanTask(ctx, sqlc.CreateAgentPlanTaskParams{
		ID:           id,
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
		return "", fmt.Errorf("create plan task %q: %w", item.ID, err)
	}
	return id, nil
}
