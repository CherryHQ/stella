package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	GoalEventPlanSubmitted      = "plan_submitted"
	GoalEventAttemptStarted     = "attempt_started"
	GoalEventAttemptFinished    = "attempt_finished"
	GoalEventAcceptanceRecorded = "acceptance_recorded"
	GoalEventLifecycleChanged   = "lifecycle_changed"
	GoalEventHumanMessage       = "human_message"
)

const attemptTimelineContextLimit = 12

// ValidGoalEventType reports whether s is a known goal timeline event type.
func ValidGoalEventType(s string) bool {
	switch s {
	case GoalEventPlanSubmitted, GoalEventAttemptStarted, GoalEventAttemptFinished,
		GoalEventAcceptanceRecorded, GoalEventLifecycleChanged, GoalEventHumanMessage:
		return true
	}
	return false
}

type PlanSubmittedPayload struct {
	Children []PlanChildSummary `json:"children"`
	Edges    []PlanEdgeSummary  `json:"edges"`
}

type PlanChildSummary struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

type PlanEdgeSummary struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	OnFailure string `json:"on_failure"`
}

type AttemptStartedPayload struct {
	Purpose   string `json:"purpose"`
	AttemptNo int64  `json:"attempt_no"`
	Status    string `json:"status"`
}

type AttemptFinishedPayload struct {
	Purpose         string `json:"purpose"`
	AttemptNo       int64  `json:"attempt_no"`
	Status          string `json:"status"`
	FailureClass    string `json:"failure_class,omitempty"`
	BlockedBy       string `json:"blocked_by,omitempty"`
	Reason          string `json:"reason,omitempty"`
	EvidenceSummary string `json:"evidence_summary,omitempty"`
}

type AcceptanceRecordedPayload struct {
	ItemID   string `json:"item_id"`
	ItemKind string `json:"item_kind"`
	Result   string `json:"result"`
	ExitCode *int64 `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type LifecycleChangedPayload struct {
	From        string `json:"from"`
	To          string `json:"to"`
	BlockReason string `json:"block_reason,omitempty"`
}

type HumanMessagePayload struct {
	Text                  string `json:"text"`
	ResponderType         string `json:"responder_type"`
	ResponderID           string `json:"responder_id"`
	ReattemptAuthorized   bool   `json:"reattempt_authorized"`
	ReattemptSkippedCause string `json:"reattempt_skipped_cause,omitempty"`
}

// TimelineContextEvent is the compact context slice frozen into a new attempt's
// input_context. It is intentionally small: the executor needs the recent facts,
// not a second copy of the whole audit log.
type TimelineContextEvent struct {
	EventType    string `json:"event_type"`
	AttemptID    string `json:"attempt_id,omitempty"`
	Status       string `json:"status,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	BlockedBy    string `json:"blocked_by,omitempty"`
	Reason       string `json:"reason,omitempty"`
	ItemID       string `json:"item_id,omitempty"`
	Result       string `json:"result,omitempty"`
	Text         string `json:"text,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type HumanMessageInput struct {
	GoalID          string
	Text            string
	ResponderUserID string
}

func (s *GoalService) appendGoalEvent(ctx context.Context, q *sqlc.Queries, goalID, attemptID, eventType string, payload any) (sqlc.AgentGoalEvent, error) {
	if goalID == "" || !ValidGoalEventType(eventType) {
		return sqlc.AgentGoalEvent{}, ErrInvalidTransition
	}
	params := sqlc.AppendGoalEventParams{
		ID:        newID(),
		GoalID:    goalID,
		AttemptID: pgnull.Text(attemptID),
		EventType: eventType,
		Payload:   marshalJSON(payload),
	}
	row, err := q.AppendGoalEvent(ctx, params)
	if err != nil {
		return sqlc.AgentGoalEvent{}, fmt.Errorf("append goal event %s: %w", eventType, err)
	}
	return row, nil
}

func planPayload(content DecompositionContent) PlanSubmittedPayload {
	p := PlanSubmittedPayload{
		Children: make([]PlanChildSummary, 0, len(content.Children)),
		Edges:    make([]PlanEdgeSummary, 0, len(content.Edges)),
	}
	for _, ch := range content.Children {
		p.Children = append(p.Children, PlanChildSummary{Key: ch.Key, Title: ch.Title, Kind: ch.Kind, Required: ch.Required})
	}
	for _, e := range content.Edges {
		p.Edges = append(p.Edges, PlanEdgeSummary{From: e.UpstreamKey, To: e.DownstreamKey, Kind: e.Kind, OnFailure: e.OnFailure})
	}
	return p
}

func (s *GoalService) transitionGoalLifecycle(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, to, blockReason string) (int64, error) {
	// The single generic lifecycle write: enforce the §2.1 state machine here so
	// no routing path can invent an edge (composite→ready and terminal
	// resurrection both shipped as bugs before this table existed). The special
	// writes (Claim/Accept/Block/Cancel) carry their from-guard in SQL.
	if !LegalLifecycleTransition(d.Kind, d.Lifecycle, to) {
		return 0, fmt.Errorf("%w: %s %s -> %s (goal %s)", ErrIllegalLifecycleMove, d.Kind, d.Lifecycle, to, d.ID)
	}
	doneReason := ""
	if to == LifecycleDone {
		doneReason = DoneReasonFailed
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   to,
		DoneReason:    doneReason,
		BlockReason:   blockReason,
		ID:            d.ID,
		FromLifecycle: d.Lifecycle,
	})
	if err != nil || rows == 0 {
		return rows, err
	}
	_, err = s.appendGoalEvent(ctx, q, d.ID, "", GoalEventLifecycleChanged, LifecycleChangedPayload{
		From:        d.Lifecycle,
		To:          to,
		BlockReason: blockReason,
	})
	return rows, err
}

func (s *GoalService) blockGoal(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, reason string) (int64, error) {
	rows, err := q.BlockGoal(ctx, sqlc.BlockGoalParams{BlockReason: reason, ID: d.ID})
	if err != nil || rows == 0 {
		return rows, err
	}
	_, err = s.appendGoalEvent(ctx, q, d.ID, d.ActiveAttemptID.String, GoalEventLifecycleChanged, LifecycleChangedPayload{
		From:        d.Lifecycle,
		To:          LifecycleBlocked,
		BlockReason: reason,
	})
	return rows, err
}

func (s *GoalService) acceptGoal(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, accepted AcceptedOutput) (int64, error) {
	rows, err := q.AcceptGoal(ctx, sqlc.AcceptGoalParams{AcceptedOutput: marshalNullJSON(accepted), ID: d.ID})
	if err != nil || rows == 0 {
		return rows, err
	}
	_, err = s.appendGoalEvent(ctx, q, d.ID, accepted.SourceAttempt, GoalEventLifecycleChanged, LifecycleChangedPayload{From: d.Lifecycle, To: LifecycleDone})
	return rows, err
}

func (s *GoalService) cancelGoal(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	if err := q.CancelGoal(ctx, d.ID); err != nil {
		return err
	}
	_, err := s.appendGoalEvent(ctx, q, d.ID, d.ActiveAttemptID.String, GoalEventLifecycleChanged, LifecycleChangedPayload{From: d.Lifecycle, To: LifecycleDone})
	return err
}

func (s *GoalService) promoteAttempt(ctx context.Context, attemptID string, leaseExpiresAt pgtype.Timestamptz) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		rows, err := q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{LeaseExpiresAt: leaseExpiresAt, ID: attemptID})
		if err != nil {
			return fmt.Errorf("promote attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		_, err = s.appendGoalEvent(ctx, q, att.GoalID, att.ID, GoalEventAttemptStarted, AttemptStartedPayload{
			Purpose:   att.Purpose,
			AttemptNo: att.AttemptNo,
			Status:    AttemptRunning,
		})
		return err
	})
}

func (s *GoalService) submitAttempt(ctx context.Context, q *sqlc.Queries, att sqlc.AgentGoalAttempt, ev AttemptEvidence, out json.RawMessage) (int64, error) {
	rows, err := q.SubmitAttempt(ctx, sqlc.SubmitAttemptParams{Evidence: marshalJSON(ev), Output: out, ID: att.ID})
	if err != nil || rows == 0 {
		return rows, err
	}
	_, err = s.appendGoalEvent(ctx, q, att.GoalID, att.ID, GoalEventAttemptFinished, AttemptFinishedPayload{
		Purpose:         att.Purpose,
		AttemptNo:       att.AttemptNo,
		Status:          AttemptSubmitted,
		EvidenceSummary: ev.Summary,
	})
	return rows, err
}

func (s *GoalService) finalizeAttempt(ctx context.Context, q *sqlc.Queries, att sqlc.AgentGoalAttempt, toStatus, reason, failureClass string, blockedByArg ...string) (int64, error) {
	blockedBy := ""
	if len(blockedByArg) > 0 {
		blockedBy = blockedByArg[0]
	}
	rows, err := q.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{ToStatus: toStatus, Error: reason, FailureClass: failureClass, ID: att.ID})
	if err != nil || rows == 0 {
		return rows, err
	}
	_, err = s.appendGoalEvent(ctx, q, att.GoalID, att.ID, GoalEventAttemptFinished, AttemptFinishedPayload{
		Purpose:      att.Purpose,
		AttemptNo:    att.AttemptNo,
		Status:       toStatus,
		FailureClass: failureClass,
		BlockedBy:    blockedBy,
		Reason:       reason,
	})
	return rows, err
}

func (s *GoalService) appendTimelineAcceptanceRecorded(ctx context.Context, q *sqlc.Queries, e sqlc.AppendAcceptanceEventParams) error {
	var exit *int64
	if e.ExitCode.Valid {
		v := e.ExitCode.Int64
		exit = &v
	}
	_, err := s.appendGoalEvent(ctx, q, e.GoalID, e.AttemptID.String, GoalEventAcceptanceRecorded, AcceptanceRecordedPayload{
		ItemID:   e.ItemID,
		ItemKind: e.ItemKind,
		Result:   e.Result,
		ExitCode: exit,
		Reason:   e.Rationale,
		Detail:   e.Scope,
	})
	return err
}

func (s *GoalService) recentTimelineContext(ctx context.Context, q *sqlc.Queries, goalID string) ([]TimelineContextEvent, error) {
	rows, err := q.ListRecentGoalEventContext(ctx, sqlc.ListRecentGoalEventContextParams{GoalID: goalID, Limit: attemptTimelineContextLimit})
	if err != nil {
		return nil, fmt.Errorf("list goal timeline context: %w", err)
	}
	slices.Reverse(rows)
	out := make([]TimelineContextEvent, 0, len(rows))
	for _, row := range rows {
		ctxEvent := TimelineContextEvent{EventType: row.EventType, AttemptID: row.AttemptID.String, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339)}
		switch row.EventType {
		case GoalEventAttemptFinished:
			var p AttemptFinishedPayload
			_ = unmarshalJSON(row.Payload, &p)
			ctxEvent.Status = p.Status
			ctxEvent.FailureClass = p.FailureClass
			ctxEvent.BlockedBy = p.BlockedBy
			ctxEvent.Reason = p.Reason
		case GoalEventAcceptanceRecorded:
			var p AcceptanceRecordedPayload
			_ = unmarshalJSON(row.Payload, &p)
			ctxEvent.ItemID = p.ItemID
			ctxEvent.Result = p.Result
			ctxEvent.Reason = p.Reason
			if ctxEvent.Reason == "" {
				ctxEvent.Reason = p.Detail
			}
		case GoalEventHumanMessage:
			var p HumanMessagePayload
			_ = unmarshalJSON(row.Payload, &p)
			ctxEvent.Text = p.Text
		}
		out = append(out, ctxEvent)
	}
	return out, nil
}

func (s *GoalService) priorGapsFromTimeline(ctx context.Context, q *sqlc.Queries, goalID string) (*Evaluation, error) {
	rows, err := q.ListRecentGoalEventContext(ctx, sqlc.ListRecentGoalEventContextParams{GoalID: goalID, Limit: attemptTimelineContextLimit})
	if err != nil {
		return nil, fmt.Errorf("list prior timeline gaps: %w", err)
	}
	gaps := Evaluation{}
	for _, row := range rows {
		if row.EventType != GoalEventAcceptanceRecorded {
			continue
		}
		var p AcceptanceRecordedPayload
		_ = unmarshalJSON(row.Payload, &p)
		if p.Result != ResultFail {
			continue
		}
		gaps.Gaps = append(gaps.Gaps, Gap{ItemID: p.ItemID, Reason: p.Reason, Detail: p.Detail})
	}
	if len(gaps.Gaps) == 0 {
		return nil, nil
	}
	slices.Reverse(gaps.Gaps)
	return &gaps, nil
}

// AddHumanMessage appends a human timeline message. A blocked goal treats the
// message as explicit authorization for one more attempt and re-enters the
// existing recovery lifecycle.
func (s *GoalService) AddHumanMessage(ctx context.Context, in HumanMessageInput) (sqlc.AgentGoalEvent, error) {
	if in.Text == "" {
		return sqlc.AgentGoalEvent{}, ErrInvalidEvidence
	}
	var out sqlc.AgentGoalEvent
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, in.GoalID)
		if err != nil {
			return err
		}
		authorize := d.Lifecycle == LifecycleBlocked
		skipped := ""
		if !authorize {
			skipped = "goal_not_blocked"
		}
		row, err := s.appendGoalEvent(ctx, q, d.ID, d.ActiveAttemptID.String, GoalEventHumanMessage, HumanMessagePayload{
			Text:                  in.Text,
			ResponderType:         ActorUser,
			ResponderID:           in.ResponderUserID,
			ReattemptAuthorized:   authorize,
			ReattemptSkippedCause: skipped,
		})
		if err != nil {
			return err
		}
		out = row
		if !authorize {
			return nil
		}
		if err := s.raiseBudgetForHumanMessage(ctx, q, d); err != nil {
			return err
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, d, recoveryLifecycle(d), "")
		if err != nil {
			return fmt.Errorf("reattempt after human message: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
	return out, err
}

func (s *GoalService) raiseBudgetForHumanMessage(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	if err := q.IncrementGoalBudgetBonus(ctx, d.ID); err != nil {
		return fmt.Errorf("raise human-message budget: %w", err)
	}
	return nil
}
