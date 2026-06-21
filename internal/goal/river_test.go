package goal

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

// river_test.go locks the durable-queue contributions whose behavior is config,
// not exercised by the convergence tests: the tick job's insert options and the
// StartDispatchTick precondition. These invariants are easy to break silently and
// would only surface in a live multi-node run, so they are pinned here.

// TestGoalTickInsertOpts pins the uniqueness contract that makes the convergence
// tick single-leader-safe: at most one QUEUED tick of this kind, deduped by state
// (NOT by args, since the payload is empty) with Completed and Running deliberately
// excluded — Completed so a finished tick never blocks the next one, Running
// (CR-002) so a tick orphaned by a hard crash cannot freeze convergence for up to
// 24h until River's stuck-job rescue.
func TestGoalTickInsertOpts(t *testing.T) {
	opts := goalTickInsertOpts()

	if opts.Queue != GoalTickQueue {
		t.Errorf("Queue = %q, want %q", opts.Queue, GoalTickQueue)
	}
	if opts.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want 1 (River does not retry the tick)", opts.MaxAttempts)
	}
	if opts.UniqueOpts.ByArgs {
		t.Error("ByArgs must be false: the tick payload is empty, so kind+state already keys a single live tick")
	}

	want := map[rivertype.JobState]bool{
		rivertype.JobStateAvailable: true,
		rivertype.JobStatePending:   true,
		rivertype.JobStateScheduled: true,
	}
	got := map[rivertype.JobState]bool{}
	for _, s := range opts.UniqueOpts.ByState {
		got[s] = true
	}
	if len(got) != len(want) {
		t.Fatalf("ByState = %v, want exactly %v", opts.UniqueOpts.ByState, want)
	}
	for s := range want {
		if !got[s] {
			t.Errorf("ByState missing %v", s)
		}
	}
	// Completed must be excluded, else a once-run tick would block convergence forever.
	if got[rivertype.JobStateCompleted] {
		t.Error("ByState must NOT include Completed: it would block every future tick")
	}
	// Running must be excluded (CR-002): a tick orphaned in 'running' by a hard crash
	// (SIGKILL/OOM) would otherwise dedupe-skip every later tick until River's 24h
	// stuck-job rescue, freezing the whole convergence engine.
	if got[rivertype.JobStateRunning] {
		t.Error("ByState must NOT include Running: a crash-orphaned running tick would freeze convergence up to 24h")
	}
}

// TestStartDispatchTickRequiresClient guards the ordering precondition: the tick
// periodic registers against the shared client, so SetRiverClient must run first.
func TestStartDispatchTickRequiresClient(t *testing.T) {
	if _, err := (&Service{}).StartDispatchTick(); err == nil {
		t.Fatal("StartDispatchTick with no River client must error, got nil")
	}
}
