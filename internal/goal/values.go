package goal

import (
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Transport-neutral goal value types.
//
// The goal Access/Service application methods return these instead of the raw
// sqlc rows so consumers (the HTTP transport, the agent tool) never depend on the
// persistence representation. Conversion happens here, at the boundary: raw sqlc
// stays private to the persistence and core-transition internals. Nullable
// columns become pointers (nil ⟺ absent, preserving the exact SQL-null / empty
// semantics the transport historically applied), timestamps are UTC, and opaque
// JSON blobs pass through as json.RawMessage (the domain never interprets them).

// Goal is the durable state of one goal.
type Goal struct {
	ID              string
	UserID          string
	AgentID         string
	RootID          string
	Depth           int64
	Position        int64
	Title           string
	Intent          string
	Kind            string
	Priority        string
	Required        bool
	ReviewPolicy    string
	Lifecycle       string
	BlockReason     string
	AcceptanceState string
	DoneReason      string
	AcceptanceSeq   int64
	AttemptCount    int64
	FlakyCount      int64
	BudgetBonus     int32
	// Nullable owner/link columns (nil ⟺ SQL NULL or empty).
	ProjectID       *string
	ParentID        *string
	ActiveAttemptID *string
	WorkflowID      *string
	WorkflowVersion *int32
	// AcceptedOutput preserves SQL-null exactly (nil ⟺ NULL, present-but-empty
	// stays a non-nil empty string) because the acceptance ledger distinguishes an
	// absent output from an empty one.
	AcceptedOutput     *string
	AcceptanceContract json.RawMessage
	ConvergencePolicy  json.RawMessage
	Context            json.RawMessage
	DispatchHint       json.RawMessage
	Plan               json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
	AcceptedAt         *time.Time
	CancelledAt        *time.Time
	ArchivedAt         *time.Time
	PlannedAt          *time.Time
}

// Attempt is one execution attempt of a goal.
type Attempt struct {
	ID              string
	GoalID          string
	UserID          string
	SessionID       string
	Purpose         string
	AttemptNo       int64
	Status          string
	Error           string
	WorkerID        string
	FailureClass    string
	RepairRounds    int32
	AgentID         *string
	ExecutorAgentID *string
	InputContext    json.RawMessage
	Evidence        json.RawMessage
	Output          json.RawMessage
	Gaps            json.RawMessage
	HeartbeatAt     *time.Time
	LeaseExpiresAt  *time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AttemptSummary is the bounded, lightweight execution state used by status
// projections. It deliberately excludes large attempt payloads.
type AttemptSummary struct {
	ID           string
	Purpose      string
	AttemptNo    int64
	Status       string
	SessionID    string
	Error        string
	FailureClass string
	StartedAt    *time.Time
	FinishedAt   *time.Time
	UpdatedAt    time.Time
}

// AcceptanceEvent is one row of a goal's acceptance ledger.
type AcceptanceEvent struct {
	ID                string
	GoalID            string
	Seq               int64
	ItemID            string
	ItemKind          string
	Result            string
	Command           string
	CacheKey          string
	Authority         string
	Rationale         string
	Scope             string
	ScopeHash         string
	AttemptID         *string
	ReviewerUserID    *string
	ReviewerAttemptID *string
	ExitCode          *int64
	Detail            json.RawMessage
	CreatedAt         time.Time
}

// TimelineEvent is one L3 timeline event of a goal.
type TimelineEvent struct {
	ID        string
	GoalID    string
	EventType string
	AttemptID *string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// AuthorizedGoal is the narrow durable identity that the cross-domain Authorize
// port vouches for: the authorized goal's owner and its bound agent. It exposes
// only the facts another domain needs to make its own decision (e.g. Workflow
// deriving a workflow's owner/target from a source goal), never the full
// persistence row.
type AuthorizedGoal struct {
	ID      string
	UserID  string
	AgentID string
}

// Edge is a dependency edge into a goal from an upstream goal.
type Edge struct {
	GoalID       string
	UpstreamID   string
	EdgeKind     string
	OnFailure    string
	WaiverReason string
	WaivedByUser *string
	WaivedAt     *time.Time
	CreatedAt    time.Time
}

// ---- conversions (sqlc row -> domain value) -------------------------------

func goalFromRow(d sqlc.AgentGoal) Goal {
	return Goal{
		ID:                 d.ID,
		UserID:             d.UserID,
		AgentID:            d.AgentID,
		RootID:             d.RootID,
		Depth:              d.Depth,
		Position:           d.Position,
		Title:              d.Title,
		Intent:             d.Intent,
		Kind:               d.Kind,
		Priority:           d.Priority,
		Required:           d.Required,
		ReviewPolicy:       d.ReviewPolicy,
		Lifecycle:          d.Lifecycle,
		BlockReason:        d.BlockReason,
		AcceptanceState:    d.AcceptanceState,
		DoneReason:         d.DoneReason,
		AcceptanceSeq:      d.AcceptanceSeq,
		AttemptCount:       d.AttemptCount,
		FlakyCount:         d.FlakyCount,
		BudgetBonus:        d.BudgetBonus,
		ProjectID:          textPtr(d.ProjectID),
		ParentID:           textPtr(d.ParentID),
		ActiveAttemptID:    textPtr(d.ActiveAttemptID),
		WorkflowID:         textPtr(d.WorkflowID),
		WorkflowVersion:    int4Ptr(d.WorkflowVersion),
		AcceptedOutput:     nullableText(d.AcceptedOutput),
		AcceptanceContract: d.AcceptanceContract,
		ConvergencePolicy:  d.ConvergencePolicy,
		Context:            d.Context,
		DispatchHint:       d.DispatchHint,
		Plan:               d.Plan,
		CreatedAt:          d.CreatedAt.UTC(),
		UpdatedAt:          d.UpdatedAt.UTC(),
		AcceptedAt:         timePtr(d.AcceptedAt),
		CancelledAt:        timePtr(d.CancelledAt),
		ArchivedAt:         timePtr(d.ArchivedAt),
		PlannedAt:          timePtr(d.PlannedAt),
	}
}

func goalsFromRows(rows []sqlc.AgentGoal) []Goal {
	out := make([]Goal, 0, len(rows))
	for _, d := range rows {
		out = append(out, goalFromRow(d))
	}
	return out
}

func attemptFromRow(a sqlc.AgentGoalAttempt) Attempt {
	return Attempt{
		ID:              a.ID,
		GoalID:          a.GoalID,
		UserID:          a.UserID,
		SessionID:       a.SessionID,
		Purpose:         a.Purpose,
		AttemptNo:       a.AttemptNo,
		Status:          a.Status,
		Error:           a.Error,
		WorkerID:        a.WorkerID,
		FailureClass:    a.FailureClass,
		RepairRounds:    a.RepairRounds,
		AgentID:         textPtr(a.AgentID),
		ExecutorAgentID: textPtr(a.ExecutorAgentID),
		InputContext:    a.InputContext,
		Evidence:        a.Evidence,
		Output:          a.Output,
		Gaps:            a.Gaps,
		HeartbeatAt:     timePtr(a.HeartbeatAt),
		LeaseExpiresAt:  timePtr(a.LeaseExpiresAt),
		StartedAt:       timePtr(a.StartedAt),
		FinishedAt:      timePtr(a.FinishedAt),
		CreatedAt:       a.CreatedAt.UTC(),
		UpdatedAt:       a.UpdatedAt.UTC(),
	}
}

func attemptsFromRows(rows []sqlc.AgentGoalAttempt) []Attempt {
	out := make([]Attempt, 0, len(rows))
	for _, a := range rows {
		out = append(out, attemptFromRow(a))
	}
	return out
}

func acceptanceEventFromRow(e sqlc.AgentGoalAcceptanceEvent) AcceptanceEvent {
	return AcceptanceEvent{
		ID:                e.ID,
		GoalID:            e.GoalID,
		Seq:               e.Seq,
		ItemID:            e.ItemID,
		ItemKind:          e.ItemKind,
		Result:            e.Result,
		Command:           e.Command,
		CacheKey:          e.CacheKey,
		Authority:         e.Authority,
		Rationale:         e.Rationale,
		Scope:             e.Scope,
		ScopeHash:         e.ScopeHash,
		AttemptID:         textPtr(e.AttemptID),
		ReviewerUserID:    textPtr(e.ReviewerUserID),
		ReviewerAttemptID: textPtr(e.ReviewerAttemptID),
		ExitCode:          int8Ptr(e.ExitCode),
		Detail:            e.Detail,
		CreatedAt:         e.CreatedAt.UTC(),
	}
}

func acceptanceEventsFromRows(rows []sqlc.AgentGoalAcceptanceEvent) []AcceptanceEvent {
	out := make([]AcceptanceEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, acceptanceEventFromRow(e))
	}
	return out
}

func timelineEventFromRow(e sqlc.AgentGoalEvent) TimelineEvent {
	return TimelineEvent{
		ID:        e.ID,
		GoalID:    e.GoalID,
		EventType: e.EventType,
		AttemptID: textPtr(e.AttemptID),
		Payload:   e.Payload,
		CreatedAt: e.CreatedAt.UTC(),
	}
}

func timelineEventsFromRows(rows []sqlc.AgentGoalEvent) []TimelineEvent {
	out := make([]TimelineEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, timelineEventFromRow(e))
	}
	return out
}

func edgeFromRow(e sqlc.AgentGoalEdge) Edge {
	return Edge{
		GoalID:       e.GoalID,
		UpstreamID:   e.UpstreamID,
		EdgeKind:     e.EdgeKind,
		OnFailure:    e.OnFailure,
		WaiverReason: e.WaiverReason,
		WaivedByUser: textPtr(e.WaivedByUser),
		WaivedAt:     timePtr(e.WaivedAt),
		CreatedAt:    e.CreatedAt.UTC(),
	}
}

func edgesFromRows(rows []sqlc.AgentGoalEdge) []Edge {
	out := make([]Edge, 0, len(rows))
	for _, e := range rows {
		out = append(out, edgeFromRow(e))
	}
	return out
}

// ---- nullable helpers -----------------------------------------------------

// textPtr collapses SQL NULL and empty string to nil, matching the transport's
// historical nullToPtr for owner/link columns.
func textPtr(t pgtype.Text) *string {
	if !t.Valid || t.String == "" {
		return nil
	}
	s := t.String
	return &s
}

// nullableText preserves SQL-null exactly: nil only when NULL, keeping a
// present-but-empty value as a non-nil empty string (used for accepted_output).
func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// timePtr returns a UTC pointer, nil for NULL or the zero time (matching the
// transport's parseTimePtr).
func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	if u.IsZero() {
		return nil
	}
	return &u
}

func int4Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
