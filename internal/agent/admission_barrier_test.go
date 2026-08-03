package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentskillpolicy"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

type barrierRunner struct {
	snapshot string
	started  chan struct{}
	release  chan struct{}
	busy     atomic.Bool
	closed   atomic.Bool
	once     sync.Once
}

func (r *barrierRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	r.busy.Store(true)
	r.once.Do(func() { close(r.started) })
	out := make(chan agentruntime.Event, 1)
	go func() {
		<-r.release
		r.busy.Store(false)
		out <- agentruntime.Event{Text: r.snapshot}
		close(out)
	}()
	return out
}
func (r *barrierRunner) Alive() bool             { return true }
func (r *barrierRunner) Busy() bool              { return r.busy.Load() }
func (r *barrierRunner) LastActivity() time.Time { return time.Now() }
func (r *barrierRunner) SystemPrompt() string    { return r.snapshot }
func (r *barrierRunner) Close() error            { r.closed.Store(true); return nil }

func newBarrierService(t *testing.T) (*Service, *agentruntime.Runtime, chan *barrierRunner) {
	t.Helper()
	runners := make(chan *barrierRunner, 8)
	factory := func(snapshot string) agentruntime.NewRunnerFunc {
		return func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			r := &barrierRunner{snapshot: snapshot, started: make(chan struct{}), release: make(chan struct{})}
			runners <- r
			return r, nil
		}
	}
	rt, err := agentruntime.New(agentruntime.Config{NewRunner: factory("old"), Memory: memorytest.New()})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return &Service{Runtime: rt, AgentID: "agent"}, rt, runners
}

func barrierInfo(id string) session.Info {
	return session.Info{ID: id, UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
}

func waitBarrierRunner(t *testing.T, runners <-chan *barrierRunner) *barrierRunner {
	t.Helper()
	select {
	case r := <-runners:
		select {
		case <-r.started:
		case <-time.After(time.Second):
			t.Fatal("runner was created but did not start")
		}
		return r
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner")
		return nil
	}
}

func waitText(t *testing.T, stream <-chan Event) string {
	t.Helper()
	select {
	case event := <-stream:
		if event.Err != nil {
			t.Fatalf("turn failed: %v", event.Err)
		}
		return event.Text
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn output")
		return ""
	}
}

// A policy mutation owns the per-Agent barrier until its committed snapshot is
// installed and old runners are stale. A waiting channel/web turn can therefore
// only be admitted against the new factory.
func TestAdmissionBarrierPolicyCommitPrecedesWaitingTurn(t *testing.T) {
	svc, rt, runners := newBarrierService(t)
	entered := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- svc.withAdmissionBarrier(func() error {
			close(entered)
			<-releaseMutation // stand-in for the row-locked DB mutation and commit
			rt.SetNewRunner(func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
				r := &barrierRunner{snapshot: "new", started: make(chan struct{}), release: make(chan struct{})}
				runners <- r
				return r, nil
			})
			return rt.InvalidateSkillPolicy()
		})
	}()
	<-entered

	admitted := make(chan (<-chan Event), 1)
	go func() {
		stream, err := svc.admit(context.Background(), barrierInfo("after-commit"), "turn")
		if err != nil {
			t.Errorf("admit waiting turn: %v", err)
			return
		}
		admitted <- stream
	}()
	select {
	case <-admitted:
		t.Fatal("turn admitted while policy mutation owned barrier")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("commit/invalidate: %v", err)
	}
	stream := <-admitted
	r := waitBarrierRunner(t, runners)
	if r.snapshot != "new" {
		t.Fatalf("post-commit turn runner snapshot = %q, want new", r.snapshot)
	}
	close(r.release)
	if got := waitText(t, stream); got != "new" {
		t.Fatalf("post-commit turn output = %q, want new", got)
	}
}

// A turn that gets through admission first may retain the old immutable
// snapshot. The mutation waits for that admission, marks its busy runner stale,
// and the next admission receives the committed snapshot.
func TestAdmissionBarrierTurnPrecedesPolicyCommitAndFailureDoesNotInvalidate(t *testing.T) {
	svc, rt, runners := newBarrierService(t)
	admitted := make(chan (<-chan Event), 1)
	turnHoldingBarrier := make(chan struct{})
	releaseAdmission := make(chan struct{})
	go func() {
		_ = svc.withAdmissionBarrier(func() error {
			stream, err := svc.admitLocked(context.Background(), barrierInfo("old-turn"), "turn")
			if err != nil {
				t.Errorf("admit old turn: %v", err)
				return err
			}
			admitted <- stream
			close(turnHoldingBarrier)
			<-releaseAdmission
			return nil
		})
	}()
	<-turnHoldingBarrier
	oldStream := <-admitted
	old := waitBarrierRunner(t, runners)

	mutationStarted := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- svc.withAdmissionBarrier(func() error {
			close(mutationStarted)
			rt.SetNewRunner(func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
				r := &barrierRunner{snapshot: "new", started: make(chan struct{}), release: make(chan struct{})}
				runners <- r
				return r, nil
			})
			return rt.InvalidateSkillPolicy()
		})
	}()
	select {
	case <-mutationStarted:
		t.Fatal("mutation acquired barrier before prior admission released it")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseAdmission)
	if err := <-mutationDone; err != nil {
		t.Fatalf("commit/invalidate: %v", err)
	}
	if !old.Busy() { // still running; it must not be closed or replaced mid-turn.
		t.Fatal("old admitted runner stopped before its turn completed")
	}
	close(old.release)
	if got := waitText(t, oldStream); got != "old" {
		t.Fatalf("pre-commit admitted turn output = %q, want old", got)
	}

	newStream, err := svc.admit(context.Background(), barrierInfo("next-turn"), "turn")
	if err != nil {
		t.Fatalf("admit next turn: %v", err)
	}
	newRunner := waitBarrierRunner(t, runners)
	if newRunner.snapshot != "new" {
		t.Fatalf("next turn runner snapshot = %q, want new", newRunner.snapshot)
	}
	close(newRunner.release)
	if got := waitText(t, newStream); got != "new" {
		t.Fatalf("next turn output = %q, want new", got)
	}

	// A rolled-back mutation releases the barrier but cannot mark/rebuild a
	// runner. Use the old session once more to prove its existing runner remains.
	failed := svc.withAdmissionBarrier(func() error { return errors.New("rollback") })
	if failed == nil {
		t.Fatal("failed mutation error = nil")
	}
	// The successful mutation made old stale, so prove no *additional* rebuild
	// occurs on failure by reusing the new runner's session.
	reused, err := svc.admit(context.Background(), barrierInfo("next-turn"), "turn")
	if err != nil {
		t.Fatalf("admit after failed mutation: %v", err)
	}
	select {
	case unexpected := <-runners:
		t.Fatalf("failed mutation rebuilt runner with snapshot %q", unexpected.snapshot)
	case <-time.After(30 * time.Millisecond):
	}
	if got := waitText(t, reused); got != "new" {
		t.Fatalf("turn after failed mutation output = %q, want reused new snapshot", got)
	}
}

// A policy mutation that begins before a Service exists must catch up the
// Service published during its commit. The startup refresh holds that Service's
// barrier first; only after it finishes may the committed refresh run.
func TestAgentSkillPolicyNoServicePublicationCatchesUpAfterStartupRefresh(t *testing.T) {
	pm := NewPoolManager(nil, nil)
	const agentID = "agent"
	svc, rt, runners := newBarrierService(t)
	mutationEntered := make(chan struct{})
	commit := make(chan struct{})
	published := make(chan struct{})
	releaseStartup := make(chan struct{})
	committedRefreshEntered := make(chan struct{})
	releaseCommittedRefresh := make(chan struct{})
	result := make(chan error, 1)
	var (
		mu        sync.Mutex
		refreshes []string
	)
	refresh := func(_ string, _ *Service) error {
		mu.Lock()
		refreshes = append(refreshes, "committed")
		mu.Unlock()
		rt.SetNewRunner(func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			r := &barrierRunner{snapshot: "committed", started: make(chan struct{}), release: make(chan struct{})}
			runners <- r
			return r, nil
		})
		close(committedRefreshEntered)
		<-releaseCommittedRefresh
		return nil
	}
	go func() {
		result <- pm.applyAgentSkillPolicyMutation(agentID, func() error {
			close(mutationEntered)
			<-commit
			return fmt.Errorf("simulated commit transport loss: %w", agentskillpolicy.ErrCommitOutcomeUnknown)
		}, refresh)
	}()
	<-mutationEntered

	go func() {
		_ = svc.withAdmissionBarrier(func() error {
			pm.mu.Lock()
			pm.services[agentID] = svc
			pm.mu.Unlock()
			close(published)
			<-releaseStartup
			mu.Lock()
			refreshes = append(refreshes, "startup-old")
			mu.Unlock()
			return nil
		})
	}()
	<-published
	close(commit)

	select {
	case err := <-result:
		t.Fatalf("policy mutation completed before startup refresh released: %v", err)
	default:
	}
	close(releaseStartup)
	<-committedRefreshEntered
	admitted := make(chan (<-chan Event), 1)
	go func() {
		stream, err := svc.admit(context.Background(), barrierInfo("after-catchup"), "turn")
		if err != nil {
			t.Errorf("waiting admission: %v", err)
			return
		}
		admitted <- stream
	}()
	select {
	case <-admitted:
		t.Fatal("admission passed while committed refresh held the barrier")
	default:
	}
	close(releaseCommittedRefresh)
	if err := <-result; !errors.Is(err, agentskillpolicy.ErrCommitOutcomeUnknown) {
		t.Fatalf("policy mutation error=%v; want ErrCommitOutcomeUnknown", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got, want := refreshes, []string{"startup-old", "committed"}; !slices.Equal(got, want) {
		t.Fatalf("refresh order=%v, want %v", got, want)
	}
	stream := <-admitted
	r := waitBarrierRunner(t, runners)
	if r.snapshot != "committed" {
		t.Fatalf("waiting admission runner snapshot=%q, want committed", r.snapshot)
	}
	close(r.release)
	if got := waitText(t, stream); got != "committed" {
		t.Fatalf("waiting admission output=%q, want committed", got)
	}
}

func TestAgentSkillPolicyUnknownCommitRefreshesBeforeReturning(t *testing.T) {
	svc, rt, runners := newBarrierService(t)
	pm := NewPoolManager(nil, nil)
	pm.services[svc.AgentID] = svc
	refreshed := false
	err := pm.applyAgentSkillPolicyMutation(svc.AgentID, func() error {
		return agentskillpolicy.ErrCommitOutcomeUnknown
	}, func(_ string, _ *Service) error {
		refreshed = true
		rt.SetNewRunner(func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			r := &barrierRunner{snapshot: "committed", started: make(chan struct{}), release: make(chan struct{})}
			runners <- r
			return r, nil
		})
		return rt.InvalidateSkillPolicy()
	})
	if !errors.Is(err, agentskillpolicy.ErrCommitOutcomeUnknown) || !refreshed {
		t.Fatalf("unknown commit result=%v refreshed=%t", err, refreshed)
	}
	stream, err := svc.admit(context.Background(), barrierInfo("unknown-commit"), "turn")
	if err != nil {
		t.Fatalf("post-unknown admission: %v", err)
	}
	r := waitBarrierRunner(t, runners)
	if r.snapshot != "committed" {
		t.Fatalf("post-unknown runner snapshot=%q, want committed", r.snapshot)
	}
	close(r.release)
	if got := waitText(t, stream); got != "committed" {
		t.Fatalf("post-unknown output=%q, want committed", got)
	}
}

func TestAgentSkillPolicyKnownMutationFailureDoesNotRefresh(t *testing.T) {
	pm := NewPoolManager(nil, nil)
	svc := &Service{AgentID: "agent"}
	pm.services[svc.AgentID] = svc
	preCommit := errors.New("write failed")
	refreshed := false
	err := pm.applyAgentSkillPolicyMutation(svc.AgentID, func() error { return preCommit }, func(string, *Service) error {
		refreshed = true
		return nil
	})
	if !errors.Is(err, preCommit) || refreshed {
		t.Fatalf("known mutation failure result=%v refreshed=%t", err, refreshed)
	}
}

type failingPolicySnapshotStore struct {
	config.Store
	err error
}

func (s failingPolicySnapshotStore) Snapshot(context.Context, string) (*config.Snapshot, error) {
	return nil, s.err
}

func TestAgentSkillPolicyUnknownCommitRefreshFailurePoisonsRuntime(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	svc, _, runners := newBarrierService(t)
	pm := NewPoolManager(failingPolicySnapshotStore{err: errors.New("snapshot unavailable")}, memorytest.New())
	pm.services[svc.AgentID] = svc

	oldStream, err := svc.admit(context.Background(), barrierInfo("old-idle"), "turn")
	if err != nil {
		t.Fatalf("seed old admission: %v", err)
	}
	old := waitBarrierRunner(t, runners)
	close(old.release)
	if got := waitText(t, oldStream); got != "old" {
		t.Fatalf("seed old output=%q, want old", got)
	}

	err = pm.ApplyAgentSkillPolicyMutation(svc.AgentID, func() error {
		return agentskillpolicy.ErrCommitOutcomeUnknown
	})
	if !errors.Is(err, agentskillpolicy.ErrCommitOutcomeUnknown) {
		t.Fatalf("unknown commit result=%v", err)
	}
	if !old.closed.Load() {
		t.Fatal("refresh failure left idle old runner live")
	}
	if _, err := svc.admit(context.Background(), barrierInfo("next-fails-closed"), "turn"); err == nil {
		t.Fatal("poisoned runtime admitted a turn with the old factory")
	}
}
