package goal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestTextPtrCollapsesNullAndEmpty(t *testing.T) {
	if textPtr(pgtype.Text{Valid: false}) != nil {
		t.Fatal("NULL text should be nil")
	}
	if textPtr(pgtype.Text{String: "", Valid: true}) != nil {
		t.Fatal("present-but-empty owner text should collapse to nil (matches nullToPtr)")
	}
	if p := textPtr(pgtype.Text{String: "x", Valid: true}); p == nil || *p != "x" {
		t.Fatalf("present text = %v, want &\"x\"", p)
	}
}

func TestNullableTextPreservesEmpty(t *testing.T) {
	if nullableText(pgtype.Text{Valid: false}) != nil {
		t.Fatal("NULL should be nil")
	}
	// accepted_output distinguishes an absent output from an empty one.
	if p := nullableText(pgtype.Text{String: "", Valid: true}); p == nil || *p != "" {
		t.Fatalf("present-but-empty = %v, want a non-nil empty string", p)
	}
}

func TestTimePtrNullZeroAndUTC(t *testing.T) {
	if timePtr(pgtype.Timestamptz{Valid: false}) != nil {
		t.Fatal("NULL timestamp should be nil")
	}
	if timePtr(pgtype.Timestamptz{Valid: true}) != nil {
		t.Fatal("zero timestamp should be nil (matches parseTimePtr)")
	}
	loc := time.FixedZone("x", 3600)
	local := time.Date(2026, 5, 6, 7, 8, 9, 0, loc)
	got := timePtr(pgtype.Timestamptz{Time: local, Valid: true})
	if got == nil || !got.Equal(local) || got.Location() != time.UTC {
		t.Fatalf("time = %v (loc %v), want the same instant in UTC", got, got.Location())
	}
}

func TestGoalFromRowNullableAndJSONSemantics(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	accepted := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	row := sqlc.AgentGoal{
		ID: "g1", UserID: "u1", AgentID: "a1", RootID: "g1",
		Depth: 2, Position: 1, Title: "T", Intent: "I", Kind: "leaf", Priority: "p2",
		Required: true, ReviewPolicy: "auto", Lifecycle: "pending", AcceptanceState: "unknown",
		AcceptanceSeq: 4, AttemptCount: 3, FlakyCount: 1, BudgetBonus: 2,
		// Owner/link nulls: empty-but-valid ProjectID collapses to nil.
		ProjectID:       pgtype.Text{String: "", Valid: true},
		ParentID:        pgtype.Text{String: "parent", Valid: true},
		WorkflowVersion: pgtype.Int4{Int32: 7, Valid: true},
		// accepted_output present-but-empty stays non-nil.
		AcceptedOutput:     pgtype.Text{String: "", Valid: true},
		AcceptanceContract: json.RawMessage(`{"policy":"judgment"}`),
		Context:            json.RawMessage(`{}`),
		CreatedAt:          created,
		UpdatedAt:          created,
		AcceptedAt:         pgtype.Timestamptz{Time: accepted, Valid: true},
		CancelledAt:        pgtype.Timestamptz{Valid: false},
	}
	g := goalFromRow(row)

	if g.ProjectID != nil {
		t.Fatalf("empty ProjectID should be nil, got %q", *g.ProjectID)
	}
	if g.ParentID == nil || *g.ParentID != "parent" {
		t.Fatalf("ParentID = %v, want &parent", g.ParentID)
	}
	if g.AcceptedOutput == nil || *g.AcceptedOutput != "" {
		t.Fatalf("AcceptedOutput = %v, want non-nil empty (absent≠empty)", g.AcceptedOutput)
	}
	if g.WorkflowVersion == nil || *g.WorkflowVersion != 7 {
		t.Fatalf("WorkflowVersion = %v, want &7", g.WorkflowVersion)
	}
	if string(g.AcceptanceContract) != `{"policy":"judgment"}` {
		t.Fatalf("AcceptanceContract JSON not passed through: %s", g.AcceptanceContract)
	}
	if g.AcceptedAt == nil || !g.AcceptedAt.Equal(accepted) {
		t.Fatalf("AcceptedAt = %v, want %v", g.AcceptedAt, accepted)
	}
	if g.CancelledAt != nil {
		t.Fatalf("CancelledAt = %v, want nil", g.CancelledAt)
	}
	if g.CreatedAt.Location() != time.UTC {
		t.Fatal("CreatedAt should be UTC")
	}
	// Scalar carry-through.
	if g.Depth != 2 || g.AcceptanceSeq != 4 || g.BudgetBonus != 2 || !g.Required {
		t.Fatalf("scalars = %+v", g)
	}
}

func TestAttemptEdgeAcceptanceConversions(t *testing.T) {
	at := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	att := attemptFromRow(sqlc.AgentGoalAttempt{
		ID: "at1", GoalID: "g1", UserID: "u1", Status: "running", RepairRounds: 3,
		AgentID:         pgtype.Text{String: "a1", Valid: true},
		ExecutorAgentID: pgtype.Text{Valid: false},
		Output:          json.RawMessage(`{"k":1}`),
		StartedAt:       pgtype.Timestamptz{Time: at, Valid: true},
		CreatedAt:       at, UpdatedAt: at,
	})
	if att.AgentID == nil || *att.AgentID != "a1" || att.ExecutorAgentID != nil {
		t.Fatalf("attempt agent ptrs = %v / %v", att.AgentID, att.ExecutorAgentID)
	}
	if att.StartedAt == nil || string(att.Output) != `{"k":1}` || att.RepairRounds != 3 {
		t.Fatalf("attempt = %+v", att)
	}

	edge := edgeFromRow(sqlc.AgentGoalEdge{
		GoalID: "g1", UpstreamID: "g0", EdgeKind: "hard", OnFailure: "block",
		WaivedByUser: pgtype.Text{Valid: false}, CreatedAt: at,
	})
	if edge.WaivedByUser != nil || edge.WaivedAt != nil || edge.GoalID != "g1" {
		t.Fatalf("edge = %+v", edge)
	}

	ev := acceptanceEventFromRow(sqlc.AgentGoalAcceptanceEvent{
		ID: "e1", GoalID: "g1", Seq: 5, ItemID: "i", ItemKind: "judgment", Result: "pass",
		ExitCode:  pgtype.Int8{Int64: 0, Valid: true},
		AttemptID: pgtype.Text{Valid: false},
		Detail:    json.RawMessage(`{"why":"ok"}`),
		CreatedAt: at,
	})
	if ev.ExitCode == nil || *ev.ExitCode != 0 || ev.AttemptID != nil {
		t.Fatalf("acceptance event ptrs = %v / %v", ev.ExitCode, ev.AttemptID)
	}
	if string(ev.Detail) != `{"why":"ok"}` || ev.Seq != 5 {
		t.Fatalf("acceptance event = %+v", ev)
	}
}
