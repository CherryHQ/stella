package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	workspaces, err := home.NewWorkspaceManager(db, config.StellaHome())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Close() })
	pm := NewPoolManager(store, memorytest.New(), WithHomeWorkspace(workspaces))
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

func lifecycleWorkspace(t *testing.T, db *pgxpool.Pool) home.Workspace {
	t.Helper()
	v, err := home.NewWorkspaceManager(db, config.StellaHome())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
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

func TestConcurrentSyncAgentPublishesOneService(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	ctx := context.Background()
	db := dbtest.New(t)
	const agentID = "concurrent-start"
	if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "newest", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pm := NewPoolManager(cfgstore.NewDBStore(db), memorytest.New(), WithHomeWorkspace(lifecycleWorkspace(t, db)))
	if err := pm.BindSessionAccess(fakePoolSessionAccess{}); err != nil {
		t.Fatal(err)
	}
	var candidates atomic.Int32
	pm.startAgentBuiltHook = func(*Service) { candidates.Add(1) }
	results := make(chan error, 2)
	go func() { results <- pm.SyncAgent(ctx, agentID) }()
	go func() { results <- pm.SyncAgent(ctx, agentID) }()
	for range 2 {
		if err := waitSyncLifecycleResult(t, results, "concurrent SyncAgent"); err != nil {
			t.Fatal(err)
		}
	}
	if candidates.Load() != 1 {
		t.Fatalf("built candidates = %d, want 1", candidates.Load())
	}
	svc := pm.GetService(agentID)
	if svc == nil {
		t.Fatal("no Service published")
	}
	if svc.lifecycle != pm.lifecycle {
		t.Fatal("production Service does not share PoolManager lifecycle gate")
	}
	pm.mu.RLock()
	count := len(pm.services)
	pm.mu.RUnlock()
	if count != 1 {
		t.Fatalf("published Services = %d, want 1", count)
	}
}

func TestStartAgentAndCloseSerializeLifecycle(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	ctx := context.Background()
	db := dbtest.New(t)
	const agentID = "start-close"
	if err := sqlc.New(db).SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pm := NewPoolManager(cfgstore.NewDBStore(db), memorytest.New(), WithHomeWorkspace(lifecycleWorkspace(t, db)))
	if err := pm.BindSessionAccess(fakePoolSessionAccess{}); err != nil {
		t.Fatal(err)
	}
	candidateReady := make(chan struct{})
	releaseCandidate := make(chan struct{})
	var once sync.Once
	pm.startAgentBuiltHook = func(*Service) {
		once.Do(func() {
			close(candidateReady)
			<-releaseCandidate
		})
	}
	started := make(chan error, 1)
	go func() { started <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, candidateReady, "start candidate")
	closed := make(chan error, 1)
	go func() { closed <- pm.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close passed an in-progress start: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseCandidate)
	if err := waitSyncLifecycleResult(t, started, "start completion"); err != nil {
		t.Fatal(err)
	}
	if err := waitSyncLifecycleResult(t, closed, "Close completion"); err != nil {
		t.Fatal(err)
	}
	if pm.GetService(agentID) != nil {
		t.Fatal("Service remained published after Close")
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
	var refreshMu sync.Mutex
	refreshes := make(map[*Service][]string)
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		refreshMu.Lock()
		refreshes[svc] = append(refreshes[svc], snap.SystemPrompt)
		refreshMu.Unlock()
	}
	reenabled := make(chan error, 1)
	go func() { reenabled <- pm.SyncAgent(ctx, agentID) }()
	close(runner.releaseClose)
	if err := waitSyncLifecycleResult(t, disabled, "disable removal"); err != nil {
		t.Fatal(err)
	}
	if err := waitSyncLifecycleResult(t, reenabled, "re-enable"); err != nil {
		t.Fatal(err)
	}
	winner := pm.GetService(agentID)
	if winner == nil || winner == old {
		t.Fatalf("winner = %p, old = %p", winner, old)
	}
	refreshMu.Lock()
	got := append([]string(nil), refreshes[winner]...)
	refreshMu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "newest-enabled" {
		t.Fatalf("winner refreshes = %v", got)
	}
}

func TestSyncAgentReenableWinsBeforeDelayedDisableReconcile(t *testing.T) {
	ctx := context.Background()
	const agentID = "reverse-disable-reenable"
	pm, store, ag := newSyncLifecyclePool(t, agentID)
	winner := pm.GetService(agentID)
	delayed := make(chan struct{})
	release := make(chan struct{})
	var delayedFirst atomic.Bool
	pm.syncAgentBeforeLifecycleHook = func() {
		if delayedFirst.CompareAndSwap(false, true) {
			close(delayed)
			<-release
		}
	}
	ag.Enabled = false
	if err := store.UpdateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	disableSync := make(chan error, 1)
	go func() { disableSync <- pm.SyncAgent(ctx, agentID) }()
	waitSyncLifecycleSignal(t, delayed, "delayed disable reconciliation")
	ag.Enabled = true
	ag.SystemPrompt = "reverse-order-newest"
	if err := store.UpdateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	var refreshMu sync.Mutex
	var prompts []string
	pm.runnerFuncRefreshedHook = func(svc *Service, snap *config.Snapshot) {
		if svc == winner {
			refreshMu.Lock()
			prompts = append(prompts, snap.SystemPrompt)
			refreshMu.Unlock()
		}
	}
	reenabled := make(chan error, 1)
	go func() { reenabled <- pm.SyncAgent(ctx, agentID) }()
	if err := waitSyncLifecycleResult(t, reenabled, "re-enable reconciliation"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := waitSyncLifecycleResult(t, disableSync, "delayed disable reconciliation"); err != nil {
		t.Fatal(err)
	}
	if pm.GetService(agentID) != winner {
		t.Fatal("delayed inactive trigger removed the enabled Service")
	}
	refreshMu.Lock()
	got := append([]string(nil), prompts...)
	refreshMu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "reverse-order-newest" {
		t.Fatalf("winner refreshes = %v", got)
	}
	stream, err := winner.admit(ctx, session.Info{ID: "winner", UserID: "user", AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn")
	if err != nil && strings.Contains(err.Error(), "runtime is closed") {
		t.Fatalf("winner closed: %v", err)
	}
	if err == nil {
		for range stream {
		}
	}
}
