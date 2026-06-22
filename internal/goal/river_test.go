package goal

import (
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

// river_test.go locks the durable-queue contributions whose behavior is config,
// not exercised by the convergence tests: the tick job's insert options and the
// StartDispatchTick precondition. These invariants are easy to break silently and
// would only surface in a live multi-node run, so they are pinned here.

// TestGoalTickInsertOpts pins the uniqueness contract that makes the convergence
// tick single-leader-safe: at most one live tick per window, deduped by state and
// period (NOT by args, since the payload is empty). River v3 forces ByState to
// include the four required states (available/pending/running/scheduled), so the
// crash-safety property CR-002 needs comes from ByPeriod instead — see
// goalTickInsertOpts. Completed/discarded stay out so a finished tick never blocks
// the next one.
func TestGoalTickInsertOpts(t *testing.T) {
	const tickInterval = 2 * time.Second
	opts := goalTickInsertOpts(tickInterval)

	if opts.Queue != GoalTickQueue {
		t.Errorf("Queue = %q, want %q", opts.Queue, GoalTickQueue)
	}
	if opts.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want 1 (River does not retry the tick)", opts.MaxAttempts)
	}
	if opts.UniqueOpts.ByArgs {
		t.Error("ByArgs must be false: the tick payload is empty, so kind+state already keys a single live tick")
	}
	// ByPeriod scopes uniqueness to a window so a crash-orphaned running tick cannot
	// freeze convergence for 24h (CR-002); it must equal the tick interval and stay
	// at/above River's 1s minimum.
	if opts.UniqueOpts.ByPeriod != tickInterval {
		t.Errorf("ByPeriod = %v, want %v (the tick interval)", opts.UniqueOpts.ByPeriod, tickInterval)
	}

	want := map[rivertype.JobState]bool{
		rivertype.JobStateAvailable: true,
		rivertype.JobStatePending:   true,
		rivertype.JobStateRunning:   true,
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
}

// TestGoalTickInsertOptsClampsPeriod guards River's 1s ByPeriod minimum: a
// sub-second tick interval must clamp up, or PeriodicJobEnqueuer fails validation.
func TestGoalTickInsertOptsClampsPeriod(t *testing.T) {
	opts := goalTickInsertOpts(200 * time.Millisecond)
	if opts.UniqueOpts.ByPeriod < time.Second {
		t.Errorf("ByPeriod = %v, want clamped to >= 1s", opts.UniqueOpts.ByPeriod)
	}
}

// TestStartDispatchTickRequiresClient guards the ordering precondition: the tick
// periodic registers against the shared client, so SetRiverClient must run first.
func TestStartDispatchTickRequiresClient(t *testing.T) {
	if _, err := (&Service{}).StartDispatchTick(); err == nil {
		t.Fatal("StartDispatchTick with no River client must error, got nil")
	}
}
