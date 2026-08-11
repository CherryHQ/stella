package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func newSyncLifecyclePool(t *testing.T, agentID string) (*PoolManager, *cfgstore.DBStore, config.Agent) {
	t.Helper()
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	db := dbtest.New(t)
	if err := sqlc.New(db).SeedAgent(context.Background(), sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "initial", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store := cfgstore.NewDBStore(db)
	pm := NewPoolManager(store, memorytest.New())
	if err := pm.BindSessionAccess(fakePoolSessionAccess{}); err != nil {
		t.Fatal(err)
	}
	if err := pm.SyncAgent(context.Background(), agentID); err != nil {
		t.Fatalf("initial SyncAgent: %v", err)
	}
	ag, err := store.GetAgent(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return pm, store, ag
}

func waitSyncLifecycleSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitSyncLifecycleResult(t *testing.T, ch <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

type racingSnapshotLoader struct{ calls atomic.Int32 }

func (l *racingSnapshotLoader) Snapshot(_ context.Context, agentID string) (*config.Snapshot, error) {
	n := l.calls.Add(1)
	return &config.Snapshot{AgentID: agentID, SystemPrompt: fmt.Sprintf("v%d", n)}, nil
}

func TestConcurrentSyncAgentPublishesOneServiceAndSynchronizesWinner(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	ctx := context.Background()
	db := dbtest.New(t)
	const agentID = "concurrent-start"
	if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	loader := &racingSnapshotLoader{}
	pm := NewPoolManager(cfgstore.NewDBStore(db), memorytest.New(), WithSnapshotLoader(loader))
	if err := pm.BindSessionAccess(fakePoolSessionAccess{}); err != nil {
		t.Fatal(err)
	}

	var (
		mu         sync.Mutex
		candidates []*Service
		refreshed  = make(map[*Service]string)
	)
	candidatesReady := make(chan struct{})
	releaseCandidates := make(chan struct{})
	loserCloseEntered := make(chan struct{})
	releaseLoserClose := make(chan struct{})
	var candidateCount atomic.Int32
	pm.startAgentCandidateHook = func(svc *Service) {
		mu.Lock()
		candidates = append(candidates, svc)
		mu.Unlock()
		if candidateCount.Add(1) == 2 {
			close(candidatesReady)
		}
		<-releaseCandidates
	}
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		mu.Lock()
		refreshed[svc] = snap.SystemPrompt
		mu.Unlock()
	}
	pm.startAgentCandidateCloseHook = func(*Service) {
		close(loserCloseEntered)
		<-releaseLoserClose
	}

	results := make(chan error, 2)
	go func() { results <- pm.SyncAgent(ctx, agentID) }()
	go func() { results <- pm.SyncAgent(ctx, agentID) }()
	<-candidatesReady
	close(releaseCandidates)
	<-loserCloseEntered
	fenceSnapshotted := make(chan struct{})
	var fenceSnapshotOnce sync.Once
	pm.homeOwnerFenceSnapshotHook = func() { fenceSnapshotOnce.Do(func() { close(fenceSnapshotted) }) }
	fenced := make(chan home.OwnerFenceLease, 1)
	go func() {
		lease, err := pm.AcquireHomeOwnerFence(ctx, home.OwnerUser, "user")
		if err != nil {
			t.Errorf("owner fence during loser close: %v", err)
			return
		}
		fenced <- lease
	}()
	<-fenceSnapshotted
	select {
	case lease := <-fenced:
		lease.Release()
		t.Fatal("owner fence passed while losing candidate close was structurally fenced")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseLoserClose)
	leaseDuringClose := <-fenced
	leaseDuringClose.Release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent SyncAgent: %v", err)
		}
	}

	winner := pm.GetService(agentID)
	if winner == nil {
		t.Fatal("no Service published")
	}
	mu.Lock()
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(candidates))
	}
	var loser *Service
	for _, candidate := range candidates {
		if candidate != winner {
			loser = candidate
		}
	}
	winnerPrompt := refreshed[winner]
	mu.Unlock()
	if loser == nil {
		t.Fatal("both starters observed the same candidate")
	}
	if _, err := loser.Runtime.ChatAdmitted(ctx, session.Info{ID: "loser", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn"); err == nil || !strings.Contains(err.Error(), "runtime is closed") {
		t.Fatalf("losing candidate admission error = %v, want closed Runtime", err)
	}
	latest := fmt.Sprintf("v%d", loader.calls.Load())
	if winnerPrompt != latest || loader.calls.Load() < 4 {
		t.Fatalf("winner refresh = %q, calls = %d, want latest %q after loser synchronization", winnerPrompt, loader.calls.Load(), latest)
	}
	if _, err := winner.Runtime.ChatAdmitted(ctx, session.Info{ID: "winner", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn"); err != nil && strings.Contains(err.Error(), "runtime is closed") {
		t.Fatalf("published winner is unusable: %v", err)
	}

	lease, err := pm.AcquireHomeOwnerFence(ctx, home.OwnerAgent, agentID)
	if err != nil {
		t.Fatalf("owner fence: %v", err)
	}
	if got := len(lease.(*homeOwnerFenceLease).services); got != 1 || lease.(*homeOwnerFenceLease).services[0] != winner {
		t.Fatalf("owner fence services = %d, want exact winner", got)
	}
	lease.Release()
}

func TestStartAgentCandidateIsClosedWhenShutdownWinsPublication(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	ctx := context.Background()
	db := dbtest.New(t)
	const agentID = "shutdown-start"
	if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store := cfgstore.NewDBStore(db)
	pm := NewPoolManager(store, memorytest.New(), WithSnapshotLoader(&racingSnapshotLoader{}))
	if err := pm.BindSessionAccess(fakePoolSessionAccess{}); err != nil {
		t.Fatal(err)
	}
	candidateReady := make(chan *Service, 1)
	releaseCandidate := make(chan struct{})
	pm.startAgentCandidateHook = func(svc *Service) {
		candidateReady <- svc
		<-releaseCandidate
	}
	started := make(chan error, 1)
	go func() { started <- pm.SyncAgent(ctx, agentID) }()
	candidate := <-candidateReady
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(releaseCandidate)
	if err := <-started; err == nil || !strings.Contains(err.Error(), "PoolManager is closing") {
		t.Fatalf("start after shutdown error = %v", err)
	}
	if pm.GetService(agentID) != nil {
		t.Fatal("candidate published after shutdown")
	}
	if _, err := candidate.Runtime.ChatAdmitted(ctx, session.Info{ID: "candidate", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn"); err == nil || !strings.Contains(err.Error(), "runtime is closed") {
		t.Fatalf("rejected candidate admission error = %v, want closed Runtime", err)
	}
}

func TestSyncAgentDisableRemovalConcurrentReenableRestartsNewestService(t *testing.T) {
	ctx := context.Background()
	const agentID = "disable-reenable"
	pm, store, ag := newSyncLifecyclePool(t, agentID)
	old := pm.GetService(agentID)
	runner := &blockingCloseRunner{closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	old.Runtime.SetNewRunner(func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) { return runner, nil })
	stream, err := old.admit(ctx, session.Info{ID: "old", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}

	ag.Enabled = false
	if err := store.UpdateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	disabled := make(chan error, 1)
	go func() { disabled <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, runner.closeEntered, "disabled Service close")

	ag.Enabled = true
	ag.SystemPrompt = "newest-enabled"
	if err := store.UpdateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	snapshotted := make(chan struct{})
	var snapshotOnce sync.Once
	pm.syncAgentServiceSnapshotHook = func(svc *Service) {
		if svc == old {
			snapshotOnce.Do(func() { close(snapshotted) })
		}
	}
	var refreshMu sync.Mutex
	refreshes := make(map[*Service][]string)
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		refreshMu.Lock()
		refreshes[svc] = append(refreshes[svc], snap.SystemPrompt)
		refreshMu.Unlock()
	}
	reenabled := make(chan error, 1)
	go func() { reenabled <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, snapshotted, "re-enabled SyncAgent old-Service snapshot")
	close(runner.releaseClose)
	if err := waitSyncLifecycleResult(t, disabled, "disable removal completion"); err != nil {
		t.Fatalf("disable removal: %v", err)
	}
	if err := waitSyncLifecycleResult(t, reenabled, "re-enable completion"); err != nil {
		t.Fatalf("re-enable SyncAgent: %v", err)
	}
	winner := pm.GetService(agentID)
	if winner == nil || winner == old {
		t.Fatalf("published Service = %p, old = %p", winner, old)
	}
	pm.mu.RLock()
	count := len(pm.services)
	pm.mu.RUnlock()
	if count != 1 {
		t.Fatalf("published Service count = %d, want 1", count)
	}
	refreshMu.Lock()
	winnerRefreshes := append([]string(nil), refreshes[winner]...)
	oldRefreshes := append([]string(nil), refreshes[old]...)
	refreshMu.Unlock()
	if len(oldRefreshes) != 0 {
		t.Fatalf("retired Service refreshed after removal: %v", oldRefreshes)
	}
	if len(winnerRefreshes) == 0 || winnerRefreshes[len(winnerRefreshes)-1] != "newest-enabled" {
		t.Fatalf("winner refreshes = %v, want final newest-enabled", winnerRefreshes)
	}
	if _, err := winner.Runtime.ChatAdmitted(ctx, session.Info{ID: "winner", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn"); err != nil && strings.Contains(err.Error(), "runtime is closed") {
		t.Fatalf("new winner is closed: %v", err)
	}
}

func TestSyncAgentRetriesReplacementDriftAgainstActualWinner(t *testing.T) {
	ctx := context.Background()
	const agentID = "replacement-drift"
	pm, store, ag := newSyncLifecyclePool(t, agentID)
	old := pm.GetService(agentID)
	var refreshMu sync.Mutex
	refreshes := make(map[*Service][]string)
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		refreshMu.Lock()
		refreshes[svc] = append(refreshes[svc], snap.SystemPrompt)
		refreshMu.Unlock()
	}
	snapshotted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	var snapshotOnce sync.Once
	pm.syncAgentServiceSnapshotHook = func(svc *Service) {
		if svc == old {
			snapshotOnce.Do(func() {
				close(snapshotted)
				<-releaseSnapshot
			})
		}
	}
	stale := make(chan error, 1)
	go func() { stale <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, snapshotted, "stale SyncAgent old-Service snapshot")
	if err := pm.removeAgent(agentID); err != nil {
		t.Fatalf("remove old Service: %v", err)
	}
	if err := pm.SyncAgent(ctx, agentID); err != nil {
		t.Fatalf("publish replacement: %v", err)
	}
	winner := pm.GetService(agentID)
	if winner == nil || winner == old {
		t.Fatal("production replacement was not published")
	}
	ag.SystemPrompt = "replacement-newest"
	if err := store.UpdateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	close(releaseSnapshot)
	if err := waitSyncLifecycleResult(t, stale, "replacement-drift completion"); err != nil {
		t.Fatalf("stale SyncAgent did not reconcile replacement: %v", err)
	}
	if pm.GetService(agentID) != winner {
		t.Fatal("stale SyncAgent replaced the actual winner")
	}
	refreshMu.Lock()
	winnerRefreshes := append([]string(nil), refreshes[winner]...)
	oldRefreshes := append([]string(nil), refreshes[old]...)
	refreshMu.Unlock()
	if len(oldRefreshes) != 0 {
		t.Fatalf("retired Service refreshed after replacement: %v", oldRefreshes)
	}
	if len(winnerRefreshes) == 0 || winnerRefreshes[len(winnerRefreshes)-1] != "replacement-newest" {
		t.Fatalf("winner refreshes = %v, want final replacement-newest", winnerRefreshes)
	}
}

func TestSyncAgentReturnsClosingErrorAfterCapturedServiceShutdown(t *testing.T) {
	ctx := context.Background()
	const agentID = "shutdown-drift"
	pm, _, _ := newSyncLifecyclePool(t, agentID)
	old := pm.GetService(agentID)
	var refreshMu sync.Mutex
	refreshes := make(map[*Service][]string)
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		refreshMu.Lock()
		refreshes[svc] = append(refreshes[svc], snap.SystemPrompt)
		refreshMu.Unlock()
	}
	snapshotted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	var snapshotOnce sync.Once
	pm.syncAgentServiceSnapshotHook = func(svc *Service) {
		if svc == old {
			snapshotOnce.Do(func() {
				close(snapshotted)
				<-releaseSnapshot
			})
		}
	}
	done := make(chan error, 1)
	go func() { done <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, snapshotted, "shutdown-drift Service snapshot")
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(releaseSnapshot)
	if err := waitSyncLifecycleResult(t, done, "shutdown-drift completion"); !errors.Is(err, errPoolManagerClosing) {
		t.Fatalf("SyncAgent shutdown drift error = %v, want %v", err, errPoolManagerClosing)
	}
	if pm.GetService(agentID) != nil {
		t.Fatal("Service remained published after shutdown")
	}
	refreshMu.Lock()
	oldRefreshes := append([]string(nil), refreshes[old]...)
	refreshMu.Unlock()
	if len(oldRefreshes) != 0 {
		t.Fatalf("closed Service refreshed during shutdown drift: %v", oldRefreshes)
	}
}
