package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/pkg/ai"
)

type blockingCloseRunner struct {
	closeEntered chan struct{}
	releaseClose chan struct{}
	closeErr     error
	closeOnce    sync.Once
}

func (*blockingCloseRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event)
	close(out)
	return out
}
func (*blockingCloseRunner) Alive() bool                  { return true }
func (*blockingCloseRunner) Busy() bool                   { return false }
func (*blockingCloseRunner) LastActivity() time.Time      { return time.Now() }
func (*blockingCloseRunner) SystemPrompt() string         { return "" }
func (*blockingCloseRunner) PluginContext() PluginContext { return PluginContext{} }
func (r *blockingCloseRunner) Close() error {
	r.closeOnce.Do(func() { close(r.closeEntered) })
	<-r.releaseClose
	return r.closeErr
}

func newBlockingCloseService(t *testing.T, id string) (*Service, *blockingCloseRunner) {
	t.Helper()
	runner := &blockingCloseRunner{closeEntered: make(chan struct{}), releaseClose: make(chan struct{})}
	rt, err := agentruntime.New(agentruntime.Config{LocalOnly: true, Memory: memorytest.New(), NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
		return runner, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{AgentID: id, Runtime: rt}
	stream, err := svc.admit(context.Background(), session.Info{ID: "session", UserID: "user", AgentID: id, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	return svc, runner
}

func TestRemoveAgentKeepsLiveRuntimeVisibleUntilCloseCompletes(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
	svc, runner := newBlockingCloseService(t, "agent")
	svc.lifecycle = pm.lifecycle
	pm.services[svc.AgentID] = svc
	removed := make(chan error, 1)
	go func() { removed <- pm.removeAgent(svc.AgentID) }()
	<-runner.closeEntered
	if pm.GetService(svc.AgentID) != svc {
		t.Fatal("service unpublished while Runtime.Close was blocked")
	}
	fenced := make(chan home.OwnerFenceLease, 1)
	go func() {
		lease, err := pm.AcquireHomeOwnerFence(context.Background(), home.OwnerUser, "user")
		if err != nil {
			t.Errorf("owner fence: %v", err)
			return
		}
		fenced <- lease
	}()
	select {
	case <-fenced:
		t.Fatal("owner fence passed a still-published live runtime")
	case <-time.After(30 * time.Millisecond):
	}
	readmitted := make(chan error, 1)
	go func() {
		_, err := svc.admit(context.Background(), session.Info{ID: "after-remove", UserID: "user", AgentID: svc.AgentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "turn")
		readmitted <- err
	}()
	select {
	case err := <-readmitted:
		t.Fatalf("raw Service admission passed lifecycle removal: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(runner.releaseClose)
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	lease := <-fenced
	if pm.GetService(svc.AgentID) != nil {
		t.Fatal("owner fence observed service after removal completed")
	}
	lease.Release()
	if err := <-readmitted; err == nil {
		t.Fatal("raw Service pointer admitted after Runtime.Close")
	}
}

func TestPoolManagerCloseKeepsLiveRuntimeVisibleAndRejectsStartState(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
	svc, runner := newBlockingCloseService(t, "agent")
	svc.lifecycle = pm.lifecycle
	pm.services[svc.AgentID] = svc
	closed := make(chan error, 1)
	go func() { closed <- pm.Close() }()
	<-runner.closeEntered
	pm.mu.RLock()
	closing, published := pm.closing, pm.services[svc.AgentID] == svc
	pm.mu.RUnlock()
	if !closing || !published {
		t.Fatalf("during Close: closing=%t published=%t", closing, published)
	}
	fenced := make(chan home.OwnerFenceLease, 1)
	go func() {
		lease, _ := pm.AcquireHomeOwnerFence(context.Background(), home.OwnerUser, "user")
		fenced <- lease
	}()
	var fence home.OwnerFenceLease
	select {
	case lease := <-fenced:
		// Close releases the lifecycle gate before slow runner teardown. The
		// owner fence may therefore make progress, while the still-published
		// Service remains visible until its close succeeds.
		if pm.GetService(svc.AgentID) != svc {
			t.Fatal("owner fence raced service removal before close completed")
		}
		fence = lease
	case <-time.After(30 * time.Millisecond):
		t.Fatal("owner fence remained blocked during slow Close")
	}
	// Releasing the fence joins the same in-flight termination after releasing
	// lifecycle exclusion. It cannot finish until the runner confirms Close.
	fenceReleased := make(chan struct{})
	go func() { fence.Release(); close(fenceReleased) }()
	close(runner.releaseClose)
	<-fenceReleased
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if pm.GetService(svc.AgentID) != nil {
		t.Fatal("owner fence observed service after shutdown")
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if !pm.closed {
		t.Fatal("PoolManager did not enter closed lifecycle state")
	}
}

func TestPoolManagerCloseKeepsFailedServiceForRetry(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New(), WithLocalExecution())
	want := errors.New("close failed")
	runner := &blockingCloseRunner{closeEntered: make(chan struct{}), releaseClose: make(chan struct{}), closeErr: want}
	rt, err := agentruntime.New(agentruntime.Config{LocalOnly: true, Memory: memorytest.New(), NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
		return runner, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Runtime: rt, AgentID: "agent", lifecycle: pm.lifecycle}
	pm.services[svc.AgentID] = svc
	stream, err := svc.admit(t.Context(), session.Info{ID: "session", UserID: "user", AgentID: "agent", Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	close(runner.releaseClose)
	if err := pm.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close = %v, want %v", err, want)
	}
	if pm.GetService(svc.AgentID) != svc {
		t.Fatal("failed service was unpublished")
	}
	runner.closeErr = nil
	if err := pm.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if pm.GetService(svc.AgentID) != nil {
		t.Fatal("successfully closed service remained published")
	}
}
