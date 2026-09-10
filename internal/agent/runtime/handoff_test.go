package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A turn whose deliverable output could not be committed must not be reported as
// a success. Before this contract the producer swallowed the transcript failure
// and the Run ended completed, which is a reply the transcript never had.
func TestTranscriptCommitFailureEndsTheRunFailed(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}

	mem := &recordingMemory{appendError: errors.New("transcript write failed")}
	rt, err := New(Config{
		Memory:    mem,
		AgentRuns: store,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return &chatFakeRunner{events: []Event{{Text: "answer the user never saw"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	handoff := NewExecutionHandoff()
	rt.chat(ctx, make(chan Event, 10), session.Info{
		ID: sessionID, UserID: uuid.NewString(), AgentID: "agent-1",
	}, "hello", chatOptions{handoff: handoff})

	outcome, ok := handoff.Outcome()
	if !ok {
		t.Fatal("the turn published no outcome")
	}
	if outcome.Status != agentrun.StatusFailed {
		t.Fatalf("outcome status = %q, want failed", outcome.Status)
	}
	if outcome.CommitErr == nil {
		t.Fatal("outcome reported success despite a failed transcript commit")
	}
	if outcome.Output.Deliverable() {
		t.Fatalf("outcome offered %q for delivery after its commit failed", outcome.Output.Text)
	}

	// The Run the caller would release must not be completable as a success.
	run, err := sqlc.New(db).GetAgentRun(ctx, handoff.Lease().Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("run status = %q, want the caller to decide (running)", run.Status)
	}
	if err := handoff.Lease().Finish(ctx, outcome.Status, outcome.Reason); err != nil {
		t.Fatalf("finish with the published outcome: %v", err)
	}
	run, err = sqlc.New(db).GetAgentRun(ctx, handoff.Lease().Guard.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentrun.StatusFailed {
		t.Fatalf("run status = %q, want failed (never completed without its output)", run.Status)
	}
	var activity string
	if err := db.QueryRow(ctx, `SELECT last_turn_result FROM ctx_conversation WHERE session_id = $1`, sessionID).Scan(&activity); err != nil {
		t.Fatal(err)
	}
	if activity != "error" {
		t.Fatalf("activity = %q, want error", activity)
	}
}

// A committed reply is published as deliverable output, including media-only
// replies, so a channel owner has something concrete to hand to its platform.
func TestCommittedReplyIsPublishedAsDeliverableOutput(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(store.Close)
	if err := store.RegisterBoot(ctx); err != nil {
		t.Fatal(err)
	}

	mem := &recordingMemory{}
	rt, err := New(Config{
		Memory:    mem,
		AgentRuns: store,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) {
			return &chatFakeRunner{events: []Event{
				{Store: ai.AssistantMessage{Content: []ai.ContentBlock{
					ai.ImageRefContent{MediaID: "media-1"},
					ai.TextContent{Text: "the answer"},
				}}},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handoff := NewExecutionHandoff()
	rt.chat(ctx, make(chan Event, 10), session.Info{
		ID: sessionID, UserID: uuid.NewString(), AgentID: "agent-1",
	}, "hello", chatOptions{handoff: handoff})

	outcome, ok := handoff.Outcome()
	if !ok {
		t.Fatal("the turn published no outcome")
	}
	if outcome.Status != agentrun.StatusCompleted || outcome.CommitErr != nil {
		t.Fatalf("outcome = status %q err %v, want completed", outcome.Status, outcome.CommitErr)
	}
	if outcome.Output.Text != "the answer" {
		t.Fatalf("output text = %q, want the committed reply", outcome.Output.Text)
	}
	if len(outcome.Output.Media) != 1 || outcome.Output.Media[0] != "media-1" {
		t.Fatalf("output media = %v, want the committed media reference", outcome.Output.Media)
	}
	if !outcome.Output.Deliverable() {
		t.Fatal("a committed reply was reported as undeliverable")
	}
}

// A media-only reply is deliverable output; requiring text would silently drop
// it from every delivery owner.
func TestMediaOnlyReplyIsDeliverable(t *testing.T) {
	outcome := TurnOutcome{
		Status: agentrun.StatusCompleted,
		Output: TurnOutput{Media: []string{"media-1"}},
	}
	if !outcome.Output.Deliverable() {
		t.Fatal("a media-only reply was reported as undeliverable")
	}
	empty := TurnOutcome{Status: agentrun.StatusCompleted}
	if empty.Output.Deliverable() {
		t.Fatal("an empty reply was reported as deliverable")
	}
}
