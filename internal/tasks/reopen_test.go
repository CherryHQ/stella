package tasks

import (
	"context"
	"testing"
)

// completeTask helper drives a task to done.
func completeTask(t *testing.T, h *testHarness, id string) {
	t.Helper()
	res, err := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s-" + id[:6]})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func TestReopen_NoDownstream_GoesToReady(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	completeTask(t, h, id)
	if err := h.svc.ReopenTask(context.Background(), id, false, SystemActor()); err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready", got)
	}
}

func TestReopen_NoCascade_DoneDownstream_Conflict(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock)
	completeTask(t, h, a)
	completeTask(t, h, b)
	err := h.svc.ReopenTask(context.Background(), a, false, SystemActor())
	if !IsConflict(err) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestReopen_Cascade_Standalone_DownstreamToReady(t *testing.T) {
	h := newHarness(t)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	c := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock)
	_ = h.svc.AddDep(context.Background(), c, b, DepKindHard, OnFailureBlock)
	completeTask(t, h, a)
	completeTask(t, h, b)
	completeTask(t, h, c)
	if err := h.svc.ReopenTask(context.Background(), a, true, SystemActor()); err != nil {
		t.Fatalf("ReopenTask cascade: %v", err)
	}
	for _, id := range []string{a, b, c} {
		if got := h.getTask(t, id).Status; got != StatusReady {
			t.Errorf("%s status=%q want ready (standalone cascade)", id, got)
		}
	}
}

func TestReopen_Cascade_SkipsArchivedDownstream(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock)
	completeTask(t, h, a)
	completeTask(t, h, b)
	if err := f.ArchiveTask(context.Background(), b, SystemActor()); err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
	if err := h.svc.ReopenTask(context.Background(), a, true, SystemActor()); err != nil {
		t.Fatalf("ReopenTask cascade: %v", err)
	}
	if got := h.getTask(t, a).Status; got != StatusReady {
		t.Errorf("a status=%q want ready", got)
	}
	// Archived downstream stays inert: not reset, still archived (CR-022).
	bt := h.getTask(t, b)
	if !bt.ArchivedAt.Valid {
		t.Errorf("archived downstream lost archived_at")
	}
	if bt.Status != StatusDone {
		t.Errorf("archived downstream status=%q want done (not resurrected)", bt.Status)
	}
}

func TestReopen_NoCascade_ArchivedDownstream_NoConflict(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	a := h.createTask(t, StatusReady)
	b := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), b, a, DepKindHard, OnFailureBlock)
	completeTask(t, h, a)
	completeTask(t, h, b)
	if err := f.ArchiveTask(context.Background(), b, SystemActor()); err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
	// b is hidden; a done archived downstream must not block the reopen (CR-022).
	if err := h.svc.ReopenTask(context.Background(), a, false, SystemActor()); err != nil {
		t.Fatalf("ReopenTask: archived downstream should not conflict, got %v", err)
	}
	if got := h.getTask(t, a).Status; got != StatusReady {
		t.Errorf("a status=%q want ready", got)
	}
	if !h.getTask(t, b).ArchivedAt.Valid {
		t.Errorf("archived downstream lost archived_at")
	}
}

func TestReopen_CancelledTask_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	_ = h.svc.Cancel(context.Background(), id, "", SystemActor())
	err := h.svc.ReopenTask(context.Background(), id, false, SystemActor())
	if err == nil || err.Error() != "cannot reopen cancelled task (D10)" {
		t.Fatalf("want rejection of cancelled, got %v", err)
	}
}
