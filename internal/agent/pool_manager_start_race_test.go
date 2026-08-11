package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
