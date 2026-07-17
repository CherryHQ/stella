package workflow

import (
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Transport-neutral workflow value types.
//
// The Workflow Access/Service application methods return these instead of raw
// sqlc rows so consumers (the HTTP transport, the agent tool, the scheduler
// adapter) never depend on the persistence representation. Conversion happens
// here at the boundary; sqlc stays private to Service/persistence. Nullable
// columns become pointers (nil ⟺ absent), timestamps are UTC, input specs are
// decoded to the domain InputSpec, run inputs to a plain map, and opaque JSON
// (acceptance contract, convergence policy, payload) passes through as
// json.RawMessage (the domain never interprets it).

// Workflow is the frozen definition of a reusable goal template.
type Workflow struct {
	ID                 string
	OwnerKind          string
	UserID             *string
	AgentID            *string
	Name               string
	Version            int32
	Intent             string
	AcceptanceContract json.RawMessage
	ConvergencePolicy  json.RawMessage
	Inputs             []InputSpec
	PayloadFormat      string
	Payload            json.RawMessage
	FullyFrozen        bool
	SourceGoalID       *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Run is one instantiation of a workflow.
type Run struct {
	ID              string
	WorkflowID      string
	WorkflowVersion int32
	IdempotencyKey  string
	RootGoalID      *string
	Status          string
	Inputs          map[string]string
	PlanHash        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// RunListItem is a run row for the list view, joined with its root goal's
// outcome fields (a claimed run with no root goal yet leaves them nil).
type RunListItem struct {
	Run
	RootLifecycle   *string
	RootBlockReason *string
	RootDoneReason  *string
}

// ---- conversions (sqlc row -> domain value) -------------------------------

func workflowFromRow(wf sqlc.AgentWorkflow) Workflow {
	return Workflow{
		ID:                 wf.ID,
		OwnerKind:          wf.OwnerKind,
		UserID:             textPtr(wf.UserID),
		AgentID:            textPtr(wf.AgentID),
		Name:               wf.Name,
		Version:            wf.Version,
		Intent:             wf.Intent,
		AcceptanceContract: wf.AcceptanceContract,
		ConvergencePolicy:  wf.ConvergencePolicy,
		Inputs:             decodeInputSpecs(wf.Inputs),
		PayloadFormat:      wf.PayloadFormat,
		Payload:            wf.Payload,
		FullyFrozen:        wf.FullyFrozen,
		SourceGoalID:       textPtr(wf.SourceGoalID),
		CreatedAt:          wf.CreatedAt.UTC(),
		UpdatedAt:          wf.UpdatedAt.UTC(),
	}
}

func workflowsFromRows(rows []sqlc.AgentWorkflow) []Workflow {
	out := make([]Workflow, 0, len(rows))
	for _, wf := range rows {
		out = append(out, workflowFromRow(wf))
	}
	return out
}

func runFromRow(r sqlc.AgentWorkflowRun) Run {
	return Run{
		ID:              r.ID,
		WorkflowID:      r.WorkflowID,
		WorkflowVersion: r.WorkflowVersion,
		IdempotencyKey:  r.IdempotencyKey,
		RootGoalID:      textPtr(r.RootGoalID),
		Status:          r.Status,
		Inputs:          decodeRunInputs(r.Inputs),
		PlanHash:        r.PlanHash,
		CreatedAt:       r.CreatedAt.UTC(),
		UpdatedAt:       r.UpdatedAt.UTC(),
	}
}

func runListItemFromRow(r sqlc.ListWorkflowRunsRow) RunListItem {
	return RunListItem{
		Run: Run{
			ID:              r.ID,
			WorkflowID:      r.WorkflowID,
			WorkflowVersion: r.WorkflowVersion,
			IdempotencyKey:  r.IdempotencyKey,
			RootGoalID:      textPtr(r.RootGoalID),
			Status:          r.Status,
			Inputs:          decodeRunInputs(r.Inputs),
			PlanHash:        r.PlanHash,
			CreatedAt:       r.CreatedAt.UTC(),
			UpdatedAt:       r.UpdatedAt.UTC(),
		},
		RootLifecycle:   textPtr(r.RootLifecycle),
		RootBlockReason: textPtr(r.RootBlockReason),
		RootDoneReason:  textPtr(r.RootDoneReason),
	}
}

func runListItemsFromRows(rows []sqlc.ListWorkflowRunsRow) []RunListItem {
	out := make([]RunListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, runListItemFromRow(r))
	}
	return out
}

// ---- helpers --------------------------------------------------------------

// textPtr collapses SQL NULL and empty string to nil, matching the transport's
// historical nullToPtr for the workflow owner/link columns.
func textPtr(t pgtype.Text) *string {
	if !t.Valid || t.String == "" {
		return nil
	}
	s := t.String
	return &s
}

// decodeRunInputs decodes the run's stored inputs into a plain map. A malformed
// or absent blob yields an empty (non-nil) map, matching the transport's prior
// best-effort unmarshal.
func decodeRunInputs(raw json.RawMessage) map[string]string {
	inputs := map[string]string{}
	_ = json.Unmarshal(raw, &inputs)
	return inputs
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
