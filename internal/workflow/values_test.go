package workflow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestTextPtrCollapsesNullAndEmpty(t *testing.T) {
	if got := textPtr(pgtype.Text{}); got != nil {
		t.Fatalf("null text = %v, want nil", got)
	}
	if got := textPtr(pgtype.Text{String: "", Valid: true}); got != nil {
		t.Fatalf("empty-but-valid text = %v, want nil", got)
	}
	got := textPtr(pgtype.Text{String: "abc", Valid: true})
	if got == nil || *got != "abc" {
		t.Fatalf("value text = %v, want ptr to \"abc\"", got)
	}
}

func TestDecodeRunInputsBestEffort(t *testing.T) {
	// nil/absent blob yields an empty, non-nil map.
	if got := decodeRunInputs(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil inputs = %v, want empty non-nil map", got)
	}
	// malformed blob also yields empty non-nil (no error surfaced).
	if got := decodeRunInputs(json.RawMessage(`not json`)); got == nil || len(got) != 0 {
		t.Fatalf("malformed inputs = %v, want empty non-nil map", got)
	}
	got := decodeRunInputs(json.RawMessage(`{"topic":"launch"}`))
	if got["topic"] != "launch" {
		t.Fatalf("valid inputs = %v, want topic=launch", got)
	}
}

func TestWorkflowFromRowSemantics(t *testing.T) {
	// A non-UTC timestamp must be normalized to UTC while preserving the instant.
	loc := time.FixedZone("UTC+8", 8*3600)
	created := time.Date(2026, 7, 17, 10, 0, 0, 0, loc)
	contract := json.RawMessage(`{"kind":"contract"}`)
	row := sqlc.AgentWorkflow{
		ID:                 "wf1",
		OwnerKind:          OwnerAgent,
		UserID:             pgtype.Text{String: "user1", Valid: true},
		AgentID:            pgtype.Text{}, // null → nil
		Name:               "daily",
		Version:            3,
		Intent:             "brief",
		AcceptanceContract: contract,
		ConvergencePolicy:  json.RawMessage(`{}`),
		Inputs:             json.RawMessage(`[{"name":"topic","required":true}]`),
		PayloadFormat:      PayloadFormatFrozenV0,
		Payload:            json.RawMessage(`{"p":1}`),
		FullyFrozen:        true,
		SourceGoalID:       pgtype.Text{String: "g1", Valid: true},
		CreatedAt:          created,
		UpdatedAt:          created,
	}
	wf := workflowFromRow(row)

	if wf.UserID == nil || *wf.UserID != "user1" {
		t.Fatalf("UserID = %v, want ptr user1", wf.UserID)
	}
	if wf.AgentID != nil {
		t.Fatalf("AgentID = %v, want nil for null column", wf.AgentID)
	}
	if wf.SourceGoalID == nil || *wf.SourceGoalID != "g1" {
		t.Fatalf("SourceGoalID = %v, want ptr g1", wf.SourceGoalID)
	}
	if len(wf.Inputs) != 1 || wf.Inputs[0].Name != "topic" || !wf.Inputs[0].Required {
		t.Fatalf("Inputs = %+v, want one required topic spec", wf.Inputs)
	}
	// Opaque JSON passes through verbatim; the domain never reinterprets it.
	if string(wf.AcceptanceContract) != string(contract) {
		t.Fatalf("AcceptanceContract = %s, want passthrough %s", wf.AcceptanceContract, contract)
	}
	if wf.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt zone = %v, want UTC", wf.CreatedAt.Location())
	}
	if !wf.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt instant = %v, want same instant as %v", wf.CreatedAt, created)
	}
}

func TestRunFromRowSemantics(t *testing.T) {
	created := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	row := sqlc.AgentWorkflowRun{
		ID:              "run1",
		WorkflowID:      "wf1",
		WorkflowVersion: 2,
		IdempotencyKey:  "same",
		RootGoalID:      pgtype.Text{}, // null → nil (claimed run, no root yet)
		Status:          RunClaimed,
		Inputs:          json.RawMessage(`{"topic":"x"}`),
		PlanHash:        "h",
		CreatedAt:       created,
		UpdatedAt:       created,
	}
	run := runFromRow(row)
	if run.RootGoalID != nil {
		t.Fatalf("RootGoalID = %v, want nil for unclaimed root", run.RootGoalID)
	}
	if run.Inputs["topic"] != "x" {
		t.Fatalf("Inputs = %v, want topic=x", run.Inputs)
	}

	row.RootGoalID = pgtype.Text{String: "g9", Valid: true}
	run = runFromRow(row)
	if run.RootGoalID == nil || *run.RootGoalID != "g9" {
		t.Fatalf("RootGoalID = %v, want ptr g9", run.RootGoalID)
	}
}

func TestRunListItemFromRowJoinsRootOutcome(t *testing.T) {
	now := time.Now().UTC()
	row := sqlc.ListWorkflowRunsRow{
		ID:              "run1",
		WorkflowID:      "wf1",
		WorkflowVersion: 1,
		IdempotencyKey:  "k",
		RootGoalID:      pgtype.Text{String: "g1", Valid: true},
		Status:          RunDone,
		Inputs:          json.RawMessage(`{}`),
		PlanHash:        "h",
		CreatedAt:       now,
		UpdatedAt:       now,
		RootLifecycle:   pgtype.Text{String: "done", Valid: true},
		RootBlockReason: pgtype.Text{}, // null → nil
		RootDoneReason:  pgtype.Text{String: "satisfied", Valid: true},
	}
	item := runListItemFromRow(row)
	if item.RootGoalID == nil || *item.RootGoalID != "g1" {
		t.Fatalf("RootGoalID = %v, want g1", item.RootGoalID)
	}
	if item.RootLifecycle == nil || *item.RootLifecycle != "done" {
		t.Fatalf("RootLifecycle = %v, want done", item.RootLifecycle)
	}
	if item.RootBlockReason != nil {
		t.Fatalf("RootBlockReason = %v, want nil", item.RootBlockReason)
	}
	if item.RootDoneReason == nil || *item.RootDoneReason != "satisfied" {
		t.Fatalf("RootDoneReason = %v, want satisfied", item.RootDoneReason)
	}
}
