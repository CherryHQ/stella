package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeQueries records the params it receives and returns configured rows/errors,
// so a test can assert the filter and pagination inputs and force a failure
// without a database.
type fakeQueries struct {
	goals    []sqlc.ListInboxGoalsRow
	runs     []sqlc.ListFailedInboxSchedulerRunsRow
	goalsErr error
	runsErr  error

	goalParams sqlc.ListInboxGoalsParams
	runParams  sqlc.ListFailedInboxSchedulerRunsParams
	calls      int
}

func (f *fakeQueries) ListInboxGoals(_ context.Context, arg sqlc.ListInboxGoalsParams) ([]sqlc.ListInboxGoalsRow, error) {
	f.calls++
	f.goalParams = arg
	return f.goals, f.goalsErr
}

func (f *fakeQueries) ListFailedInboxSchedulerRuns(_ context.Context, arg sqlc.ListFailedInboxSchedulerRunsParams) ([]sqlc.ListFailedInboxSchedulerRunsRow, error) {
	f.runParams = arg
	return f.runs, f.runsErr
}

func userAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("u1"), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func goalRow(id string, updated time.Time) sqlc.ListInboxGoalsRow {
	return sqlc.ListInboxGoalsRow{
		ID: id, AgentID: "a1", Title: "goal " + id,
		Lifecycle: goal.LifecycleBlocked, BlockReason: goal.BlockNeedsVerdict,
		UpdatedAt: updated,
	}
}

func TestListFailsClosedForNonUserActorBeforeQuery(t *testing.T) {
	fq := &fakeQueries{}
	svc := &Service{q: fq}

	if _, _, err := svc.List(context.Background(), authz.Authority{}, "", 0, 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid authority = %v, want ErrForbidden", err)
	}
	sys, err := authz.NewSystemAuthority(authz.Component("t"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.List(context.Background(), sys, "", 0, 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("system actor = %v, want ErrForbidden", err)
	}
	if fq.calls != 0 {
		t.Fatalf("queries ran %d times for a denied actor, want 0", fq.calls)
	}
}

func TestListQueryFailurePropagates(t *testing.T) {
	svc := &Service{q: &fakeQueries{goalsErr: errors.New("goals down")}}
	if _, _, err := svc.List(context.Background(), userAuthority(t), "", 0, 20); err == nil {
		t.Fatal("goals query failure should propagate")
	}
	svc = &Service{q: &fakeQueries{runsErr: errors.New("runs down")}}
	if _, _, err := svc.List(context.Background(), userAuthority(t), "", 0, 20); err == nil {
		t.Fatal("runs query failure should propagate")
	}
}

func TestListPassesUserScopeAndAgentFilter(t *testing.T) {
	fq := &fakeQueries{}
	svc := &Service{q: fq}
	if _, _, err := svc.List(context.Background(), userAuthority(t), "a1", 0, 20); err != nil {
		t.Fatalf("List: %v", err)
	}
	if fq.goalParams.UserID != "u1" {
		t.Fatalf("goal user scope = %q, want u1", fq.goalParams.UserID)
	}
	if !fq.goalParams.AgentID.Valid || fq.goalParams.AgentID.String != "a1" {
		t.Fatalf("goal agent filter = %+v, want a1", fq.goalParams.AgentID)
	}
	if fq.runParams.AgentID.String != "a1" || fq.runParams.UserID.String != "u1" {
		t.Fatalf("run scope/filter = %+v/%+v", fq.runParams.UserID, fq.runParams.AgentID)
	}

	// An empty filter leaves the agent param unset (all agents).
	fq2 := &fakeQueries{}
	svc2 := &Service{q: fq2}
	if _, _, err := svc2.List(context.Background(), userAuthority(t), "", 0, 20); err != nil {
		t.Fatalf("List: %v", err)
	}
	if fq2.goalParams.AgentID.Valid {
		t.Fatalf("empty filter should leave agent param unset, got %+v", fq2.goalParams.AgentID)
	}
}

func TestListStableSortNewestFirstWithIDTiebreak(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fq := &fakeQueries{goals: []sqlc.ListInboxGoalsRow{
		goalRow("g-older", t0),
		goalRow("g-a", t0.Add(time.Hour)), // same time as g-b
		goalRow("g-b", t0.Add(time.Hour)),
	}}
	svc := &Service{q: fq}
	items, _, err := svc.List(context.Background(), userAuthority(t), "", 0, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	// Newest first; ties broken by id descending. IDs are "review:<goalID>".
	if items[0].ID != "review:g-b" || items[1].ID != "review:g-a" {
		t.Fatalf("tie order = %q,%q, want review:g-b, review:g-a", items[0].ID, items[1].ID)
	}
	if items[2].ID != "review:g-older" {
		t.Fatalf("last = %q, want the older goal", items[2].ID)
	}
}

func TestListPaginationOffsetAndHasMore(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]sqlc.ListInboxGoalsRow, 0, 5)
	for i := range 5 {
		rows = append(rows, goalRow(string(rune('a'+i)), t0.Add(time.Duration(i)*time.Hour)))
	}
	svc := &Service{q: &fakeQueries{goals: rows}}

	// First page of 2: two items, more remain.
	items, hasMore, err := svc.List(context.Background(), userAuthority(t), "", 0, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || !hasMore {
		t.Fatalf("page1 = %d items hasMore=%v, want 2/true", len(items), hasMore)
	}
	// Offset past the end yields an empty (non-nil) page and no more.
	empty, hasMore, err := svc.List(context.Background(), userAuthority(t), "", 100, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if empty == nil || len(empty) != 0 || hasMore {
		t.Fatalf("offset past end = %v hasMore=%v, want empty non-nil/false", empty, hasMore)
	}
	// Last partial page: one item, no more.
	last, hasMore, err := svc.List(context.Background(), userAuthority(t), "", 4, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(last) != 1 || hasMore {
		t.Fatalf("last page = %d hasMore=%v, want 1/false", len(last), hasMore)
	}

	// A crafted token must not wrap the sqlc int32 limit into a negative LIMIT.
	if _, _, err := svc.List(context.Background(), userAuthority(t), "", 1<<31-1, 2); !errors.Is(err, ErrInvalidPage) {
		t.Fatalf("overflow page err = %v, want ErrInvalidPage", err)
	}
}

func TestSchedulerRunItemFinishTimeAndTarget(t *testing.T) {
	started := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	row := sqlc.ListFailedInboxSchedulerRunsRow{
		RunID: "r1", JobID: "job1", Name: "nightly", Error: "boom",
		AgentID:    pgtype.Text{String: "a1", Valid: true},
		StartedAt:  started,
		FinishedAt: pgtype.Timestamptz{Time: finished, Valid: true},
	}
	item := schedulerRunItem(row)
	if item.CreatedAt != finished {
		t.Fatalf("createdAt = %v, want finish time", item.CreatedAt)
	}
	if item.TargetPath != "/agents/a1/goals/schedules/job1" {
		t.Fatalf("target = %q", item.TargetPath)
	}
	if item.Kind != KindFailed || item.Source != SourceSchedulerRun {
		t.Fatalf("kind/source = %q/%q", item.Kind, item.Source)
	}

	// No finish time -> falls back to start time; no agent -> generic target.
	row.FinishedAt = pgtype.Timestamptz{}
	row.AgentID = pgtype.Text{}
	item = schedulerRunItem(row)
	if item.CreatedAt != started.UTC() {
		t.Fatalf("createdAt fallback = %v, want start time", item.CreatedAt)
	}
	if item.TargetPath != "/agents" {
		t.Fatalf("target without agent = %q, want /agents", item.TargetPath)
	}
}
