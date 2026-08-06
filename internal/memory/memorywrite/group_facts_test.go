package memorywrite_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestApplyGroupFactPlanVersionsOperationsAndCheckpointsAtomically(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)

	result, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 12,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{
			{
				Action:     memory.GroupFactActionCreate,
				Subject:    memory.GroupFactSubjectGroup,
				NewContent: "Production releases require two approvals.",
			},
			{
				Action:     memory.GroupFactActionCreate,
				Subject:    memory.GroupFactSubjectHuman,
				SubjectID:  "alice",
				NewContent: "Owns production release coordination.",
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	if result.Version != 2 || result.ChangedOperations != 2 {
		t.Fatalf("result = %#v, want version=2 changed=2", result)
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("active facts = %d, want 2", len(facts))
	}
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{GroupID: groupID, Pipeline: "group_reflect"})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor.LastSeq != 12 {
		t.Fatalf("cursor = %d, want 12", cursor.LastSeq)
	}
	logs, err := q.ListGroupFactChangelog(ctx, sqlc.ListGroupFactChangelogParams{GroupID: groupID, LimitCount: 10})
	if err != nil {
		t.Fatalf("list changelog: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("changelog rows = %d, want 2", len(logs))
	}
}

func TestApplyGroupFactPlanReplaceManyUsesOneVersion(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)

	initial, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 2,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{
			{Action: memory.GroupFactActionCreate, Subject: memory.GroupFactSubjectHuman, SubjectID: "alice", NewContent: "Coordinates API releases."},
			{Action: memory.GroupFactActionCreate, Subject: memory.GroupFactSubjectHuman, SubjectID: "alice", NewContent: "Coordinates database releases."},
		}},
	})
	if err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	if len(initial.CreatedFactIDs) != 2 {
		t.Fatalf("created ids = %v", initial.CreatedFactIDs)
	}

	replaced, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 3,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:        memory.GroupFactActionReplaceMany,
			Subject:       memory.GroupFactSubjectHuman,
			SubjectID:     "alice",
			TargetFactIDs: initial.CreatedFactIDs,
			NewContent:    "Coordinates all production releases.",
		}}},
	})
	if err != nil {
		t.Fatalf("replace facts: %v", err)
	}
	if replaced.Version != initial.Version+1 {
		t.Fatalf("replace version = %d, want %d", replaced.Version, initial.Version+1)
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 1 || facts[0].Content != "Coordinates all production releases." {
		t.Fatalf("active facts = %#v", facts)
	}
}

func TestApplyGroupFactPlanRetryAndEmptyPlanAreCheckpointNoops(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)
	req := memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 7,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:     memory.GroupFactActionCreate,
			Subject:    memory.GroupFactSubjectGroup,
			NewContent: "Use the shared staging environment.",
		}}},
	}
	first, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, req)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	retry, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, req)
	if err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	if !retry.CheckpointNoop || retry.Version != first.Version {
		t.Fatalf("retry result = %#v, first = %#v", retry, first)
	}

	empty, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 8,
	})
	if err != nil {
		t.Fatalf("empty checkpoint: %v", err)
	}
	if !empty.CheckpointNoop || empty.Version != first.Version {
		t.Fatalf("empty result = %#v", empty)
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("active facts = %d, want 1", len(facts))
	}
}

func TestApplyGroupFactPlanDeprecateManyUsesOneVersion(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)

	initial, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 2,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{
			{Action: memory.GroupFactActionCreate, Subject: memory.GroupFactSubjectHuman, SubjectID: "alice", NewContent: "Coordinates API releases."},
			{Action: memory.GroupFactActionCreate, Subject: memory.GroupFactSubjectHuman, SubjectID: "alice", NewContent: "Coordinates database releases."},
		}},
	})
	if err != nil {
		t.Fatalf("seed facts: %v", err)
	}

	result, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 3,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:        memory.GroupFactActionDeprecateMany,
			Subject:       memory.GroupFactSubjectHuman,
			SubjectID:     "alice",
			TargetFactIDs: initial.CreatedFactIDs,
		}}},
	})
	if err != nil {
		t.Fatalf("deprecate facts: %v", err)
	}
	if result.Version != initial.Version+1 || result.ChangedOperations != 1 {
		t.Fatalf("deprecate result = %#v", result)
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("active facts = %#v, want none", facts)
	}
	logs, err := q.ListGroupFactChangelog(ctx, sqlc.ListGroupFactChangelogParams{GroupID: groupID, LimitCount: 10})
	if err != nil {
		t.Fatalf("list changelog: %v", err)
	}
	deprecations := 0
	for _, row := range logs {
		if row.Action == "deprecate" {
			deprecations++
			if row.GroupVersionBefore != initial.Version || row.GroupVersionAfter != result.Version {
				t.Fatalf("deprecation versions = %d -> %d", row.GroupVersionBefore, row.GroupVersionAfter)
			}
		}
	}
	if deprecations != 2 {
		t.Fatalf("deprecation changelog rows = %d, want 2", deprecations)
	}
}

func TestApplyGroupFactPlanRollsBackEarlierOperationsWhenTargetChanged(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)

	initial, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 1,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:     memory.GroupFactActionCreate,
			Subject:    memory.GroupFactSubjectHuman,
			SubjectID:  "alice",
			NewContent: "Coordinates API releases.",
		}}},
	})
	if err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	if _, err := q.DeprecateGroupFact(ctx, sqlc.DeprecateGroupFactParams{
		GroupID: groupID,
		ID:      initial.CreatedFactIDs[0],
	}); err != nil {
		t.Fatalf("change target before write: %v", err)
	}

	_, err = memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 2,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{
			{
				Action:     memory.GroupFactActionCreate,
				Subject:    memory.GroupFactSubjectGroup,
				NewContent: "Production releases require two approvals.",
			},
			{
				Action:        memory.GroupFactActionDeprecateMany,
				Subject:       memory.GroupFactSubjectHuman,
				SubjectID:     "alice",
				TargetFactIDs: initial.CreatedFactIDs,
			},
		}},
	})
	if err == nil {
		t.Fatal("plan should fail when a target changed after reconciliation")
	}
	version, err := q.GetGroupMemoryVersion(ctx, groupID)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != initial.Version {
		t.Fatalf("version = %d, want unchanged %d", version, initial.Version)
	}
	cursor, err := q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{GroupID: groupID, Pipeline: "group_reflect"})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if cursor.LastSeq != 1 {
		t.Fatalf("cursor = %d, want unchanged 1", cursor.LastSeq)
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("earlier create escaped rolled-back window: %#v", facts)
	}
}

func TestApplyGroupFactPlanConcurrentRetryWritesOnce(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)
	req := memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   groupID,
		Pipeline:  "group_reflect",
		Watermark: 7,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:     memory.GroupFactActionCreate,
			Subject:    memory.GroupFactSubjectGroup,
			NewContent: "Production releases require two approvals.",
		}}},
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			_, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, req)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent retry: %v", err)
		}
	}
	facts, err := q.ListActiveGroupFacts(ctx, groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("active facts = %d, want 1", len(facts))
	}
	version, err := q.GetGroupMemoryVersion(ctx, groupID)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
}

func TestApplyGroupFactPlanDoesNotMutateRequest(t *testing.T) {
	db, q := openTestDB(t)
	ctx := context.Background()
	groupID := uuid.NewString()
	seedGroup(t, q, groupID)
	req := memorywrite.ApplyGroupFactPlanRequest{
		GroupID:   " " + groupID + " ",
		Pipeline:  " group_reflect ",
		Watermark: 1,
		Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
			Action:     memory.GroupFactActionCreate,
			Subject:    memory.GroupFactSubjectHuman,
			SubjectID:  " alice ",
			NewContent: " Coordinates production releases. ",
		}}},
	}

	if _, err := memorywrite.ApplyGroupFactPlan(ctx, db, q, req); err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	operation := req.Plan.Operations[0]
	if req.GroupID != " "+groupID+" " || req.Pipeline != " group_reflect " ||
		operation.SubjectID != " alice " || operation.NewContent != " Coordinates production releases. " {
		t.Fatalf("request mutated: %#v", req)
	}
}
