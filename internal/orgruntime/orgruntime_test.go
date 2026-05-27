package orgruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

type fakeStore struct {
	config.Store
	agents []config.Agent
	err    error
}

func (s *fakeStore) ListEnabledAgents(_ context.Context) ([]config.Agent, error) {
	return s.agents, s.err
}

type fakeSyncer struct {
	mu      sync.Mutex
	synced  []string
	failFor map[string]bool
}

func (s *fakeSyncer) SyncAgent(_ context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFor[agentID] {
		return errors.New("sync failed")
	}
	s.synced = append(s.synced, agentID)
	return nil
}

type fakeChannels struct {
	started bool
	err     error
}

func (c *fakeChannels) StartChannels(_ context.Context) error {
	c.started = true
	return c.err
}

func TestGetOrInit_CachesRuntime(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{{ID: "a1"}}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	rt1, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	rt2, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if rt1 != rt2 {
		t.Error("expected same OrgRuntime instance for same orgID")
	}
	if len(syncer.synced) != 1 {
		t.Errorf("expected 1 SyncAgent call, got %d", len(syncer.synced))
	}
}

func TestGetOrInit_SyncsAllAgents(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{{ID: "a1"}, {ID: "a2"}, {ID: "a3"}}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	_, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if len(syncer.synced) != 3 {
		t.Errorf("expected 3 SyncAgent calls, got %d", len(syncer.synced))
	}
}

func TestGetOrInit_StartsChannels(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{}}
	syncer := &fakeSyncer{}
	channels := &fakeChannels{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer, Channels: channels})

	_, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if !channels.started {
		t.Error("expected channels to be started")
	}
}

func TestGetOrInit_NilChannels(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	_, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetOrInit_ListAgentsError(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	_, err := mgr.GetOrInit(context.Background(), "org1")
	if err == nil {
		t.Fatal("expected error")
	}

	// After failure, cache entry is removed so a fresh OrgRuntime is created on retry.
	store.err = nil
	store.agents = []config.Agent{{ID: "a1"}}
	rt, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if rt.OrgID() != "org1" {
		t.Errorf("expected org1, got %s", rt.OrgID())
	}
}

func TestGetOrInit_NoImplicitSeeding(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	rt, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if rt.OrgID() != "org1" {
		t.Errorf("expected org1, got %s", rt.OrgID())
	}
}

func TestGetOrInit_Concurrent(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{{ID: "a1"}}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	var wg sync.WaitGroup
	results := make([]*OrgRuntime, 10)
	errs := make([]error, 10)
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = mgr.GetOrInit(context.Background(), "org1")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d got error: %v", i, err)
		}
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Error("expected all goroutines to get the same OrgRuntime")
		}
	}
}

func TestGetOrInit_DifferentOrgs(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{{ID: "a1"}}}
	syncer := &fakeSyncer{}
	mgr := NewManager(ManagerDeps{Store: store, Syncer: syncer})

	rt1, err := mgr.GetOrInit(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	rt2, err := mgr.GetOrInit(context.Background(), "org2")
	if err != nil {
		t.Fatal(err)
	}
	if rt1 == rt2 {
		t.Error("expected different OrgRuntime instances for different orgIDs")
	}
}

func TestStart_Idempotent(t *testing.T) {
	store := &fakeStore{agents: []config.Agent{{ID: "a1"}}}
	syncer := &fakeSyncer{}
	rt := &OrgRuntime{orgID: "org1"}

	if err := rt.Start(context.Background(), store, syncer, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), store, syncer, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(syncer.synced) != 1 {
		t.Errorf("expected 1 SyncAgent call (idempotent), got %d", len(syncer.synced))
	}
}
