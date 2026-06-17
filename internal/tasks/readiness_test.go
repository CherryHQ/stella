package tasks

import (
	"database/sql"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// task builds a minimal AgentTask for table tests. Only fields readiness
// actually reads are populated.
func task(status string, notBefore string) sqlc.AgentTask {
	t := sqlc.AgentTask{Status: status}
	if notBefore != "" {
		ts, _ := time.Parse(time.RFC3339, notBefore)
		t.NotBefore = sql.NullTime{Time: ts, Valid: true}
	}
	return t
}

func hard(upstream, onFailure string, waived bool) DepEdgeView {
	return DepEdgeView{DepTaskID: "u", Kind: DepKindHard, OnFailure: onFailure, UpstreamStatus: upstream, Waived: waived}
}

func soft(upstream string) DepEdgeView {
	return DepEdgeView{DepTaskID: "u", Kind: DepKindSoft, OnFailure: "", UpstreamStatus: upstream}
}

func TestCompute_StatusShortCircuits(t *testing.T) {
	now := time.Now()
	cases := []struct {
		status string
		want   string
	}{
		{StatusDraft, ReadinessDraft},
		{StatusRunning, ReadinessRunning},
		{StatusBlocked, ReadinessBlocked},
		{StatusDone, ReadinessTerminal},
		{StatusFailed, ReadinessTerminal},
		{StatusCancelled, ReadinessTerminal},
	}
	for _, tc := range cases {
		got := Compute(task(tc.status, ""), nil, now)
		if got.State != tc.want {
			t.Errorf("status=%s: got state=%q want %q", tc.status, got.State, tc.want)
		}
		if got.Dispatchable {
			t.Errorf("status=%s: should not be dispatchable", tc.status)
		}
	}
}

func TestCompute_ReadyNoDeps_IsDispatchable(t *testing.T) {
	r := Compute(task(StatusReady, ""), nil, time.Now())
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("want dispatchable, got %+v", r)
	}
}

func TestCompute_NotBeforeDefers(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)
	r := Compute(task(StatusReady, future), nil, now)
	if r.State != ReadinessDeferred || r.Dispatchable {
		t.Fatalf("expected deferred, got %+v", r)
	}
	past := now.Add(-time.Hour).Format(time.RFC3339)
	r = Compute(task(StatusReady, past), nil, now)
	if !r.Dispatchable {
		t.Fatalf("past not_before should not defer, got %+v", r)
	}
}

// D11 dep table — one test case per row.
func TestCompute_DepTable(t *testing.T) {
	now := time.Now()
	type tc struct {
		name string
		deps []DepEdgeView
		want string
		disp bool
	}
	cases := []tc{
		{"hard pending", []DepEdgeView{hard(StatusRunning, OnFailureBlock, false)}, ReadinessWaitingDeps, false},
		{"hard done", []DepEdgeView{hard(StatusDone, OnFailureBlock, false)}, ReadinessDispatchable, true},
		{"hard failed ignore", []DepEdgeView{hard(StatusFailed, OnFailureIgnore, false)}, ReadinessDispatchable, true},
		{"hard cancelled ignore", []DepEdgeView{hard(StatusCancelled, OnFailureIgnore, false)}, ReadinessDispatchable, true},
		{"hard failed block no waiver", []DepEdgeView{hard(StatusFailed, OnFailureBlock, false)}, ReadinessBlocked, false},
		{"hard failed block waived", []DepEdgeView{hard(StatusFailed, OnFailureBlock, true)}, ReadinessDispatchable, true},
		{"hard failed propagate", []DepEdgeView{hard(StatusFailed, OnFailureFail, false)}, ReadinessBlocked, false},
		{"soft pending", []DepEdgeView{soft(StatusRunning)}, ReadinessWaitingDeps, false},
		{"soft done", []DepEdgeView{soft(StatusDone)}, ReadinessDispatchable, true},
		{"soft failed", []DepEdgeView{soft(StatusFailed)}, ReadinessDispatchable, true},
		{"soft cancelled", []DepEdgeView{soft(StatusCancelled)}, ReadinessDispatchable, true},
		{"unknown kind fails closed", []DepEdgeView{{DepTaskID: "u", Kind: "hrd", UpstreamStatus: StatusDone}}, ReadinessWaitingDeps, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Compute(task(StatusReady, ""), c.deps, now)
			if r.State != c.want || r.Dispatchable != c.disp {
				t.Errorf("got %+v want state=%s disp=%v", r, c.want, c.disp)
			}
		})
	}
}

func TestCompute_MultipleDeps_ReportsAllBlockingReasons(t *testing.T) {
	deps := []DepEdgeView{
		{DepTaskID: "a", Kind: DepKindHard, OnFailure: OnFailureBlock, UpstreamStatus: StatusRunning},
		{DepTaskID: "b", Kind: DepKindHard, OnFailure: OnFailureBlock, UpstreamStatus: StatusReady},
	}
	r := Compute(task(StatusReady, ""), deps, time.Now())
	if r.State != ReadinessWaitingDeps {
		t.Fatalf("want waiting_deps, got %q", r.State)
	}
	if len(r.Reasons) != 2 {
		t.Fatalf("want 2 reasons, got %d (%+v)", len(r.Reasons), r.Reasons)
	}
}

func TestCompute_MixedSatisfiedAndFailedBlock_BlocksAndReports(t *testing.T) {
	deps := []DepEdgeView{
		{DepTaskID: "ok", Kind: DepKindHard, OnFailure: OnFailureBlock, UpstreamStatus: StatusDone},
		{DepTaskID: "bad", Kind: DepKindHard, OnFailure: OnFailureBlock, UpstreamStatus: StatusFailed},
	}
	r := Compute(task(StatusReady, ""), deps, time.Now())
	if r.State != ReadinessBlocked || r.Dispatchable {
		t.Fatalf("want blocked + not dispatchable, got %+v", r)
	}
}

func TestCompute_IsPure(t *testing.T) {
	// Sanity: same inputs -> same output (no time-dependent state besides `now`,
	// no global mutation). This is mostly a regression guard if someone is
	// tempted to call out to a clock or a DB.
	tk := task(StatusReady, "")
	deps := []DepEdgeView{hard(StatusDone, OnFailureBlock, false)}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r1 := Compute(tk, deps, now)
	r2 := Compute(tk, deps, now)
	if r1.State != r2.State || r1.Dispatchable != r2.Dispatchable {
		t.Fatalf("not pure: %+v vs %+v", r1, r2)
	}
}
