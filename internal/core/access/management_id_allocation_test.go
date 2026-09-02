package access

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/platform/config"
)

type occupancyResult struct {
	occupied bool
	err      error
}

func (o occupancyResult) AgentIDOccupied(context.Context, string) (bool, error) {
	return o.occupied, o.err
}

type baseOnlyOccupancy struct{}

func (baseOnlyOccupancy) AgentIDOccupied(_ context.Context, id string) (bool, error) {
	return id == "agent", nil
}

type lookupAgents struct{ err error }

func (f lookupAgents) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{}, f.err
}
func (lookupAgents) CreateAgent(context.Context, config.Agent) error { return nil }
func (lookupAgents) UpdateAgent(context.Context, config.Agent) error { return nil }
func (lookupAgents) DeleteAgent(context.Context, string) error       { return nil }

func TestUniqueAgentIDFailsClosed(t *testing.T) {
	transient := errors.New("database unavailable")
	fsErr := errors.New("filesystem unavailable")
	tests := []struct {
		name      string
		dbErr     error
		occupancy AgentIDOccupancy
		want      string
	}{
		{name: "wrapped no rows is free", dbErr: fmt.Errorf("lookup: %w", pgx.ErrNoRows), occupancy: occupancyResult{}, want: "agent"},
		{name: "transient database", dbErr: transient, occupancy: occupancyResult{}},
		{name: "canceled database", dbErr: context.Canceled, occupancy: occupancyResult{}},
		{name: "nil filesystem checker", dbErr: pgx.ErrNoRows},
		{name: "filesystem error", dbErr: pgx.ErrNoRows, occupancy: occupancyResult{err: fsErr}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Management{agents: lookupAgents{err: tt.dbErr}, occupancy: tt.occupancy}
			got, err := m.uniqueAgentID(t.Context(), "agent")
			if tt.want != "" {
				if err != nil || got != tt.want {
					t.Fatalf("got (%q, %v), want %q", got, err, tt.want)
				}
			} else if err == nil {
				t.Fatalf("got free ID %q, want fail closed", got)
			}
		})
	}
}

func TestUniqueAgentIDFilesystemOccupancyUsesNextSuffix(t *testing.T) {
	m := &Management{agents: lookupAgents{err: pgx.ErrNoRows}, occupancy: baseOnlyOccupancy{}}
	got, err := m.uniqueAgentID(t.Context(), "agent")
	if err != nil || got != "agent-2" {
		t.Fatalf("got (%q, %v), want agent-2", got, err)
	}
}

type concurrentAgents struct {
	mu     sync.Mutex
	agents map[string]config.Agent
}

func (f *concurrentAgents) GetAgent(_ context.Context, id string) (config.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return config.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}
func (f *concurrentAgents) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }
func (f *concurrentAgents) CreateAgent(_ context.Context, a config.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.agents[a.ID]; exists {
		return errors.New("duplicate")
	}
	f.agents[a.ID] = a
	return nil
}
func (f *concurrentAgents) UpdateAgent(context.Context, config.Agent) error { return nil }
func (f *concurrentAgents) DeleteAgent(context.Context, string) error       { return nil }

func TestManagementConcurrentCreateSerializesIDAllocationAndInsert(t *testing.T) {
	agents := &concurrentAgents{agents: map[string]config.Agent{}}
	assign := newFakeAssign()
	m := NewManagement(NewService(agents, assign), agents, assign, nil, nil, nil, nil, nil, nil, WithAgentIDOccupancy(freeAgentIDOccupancy{}))
	start := make(chan struct{})
	results := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			a, err := m.Create(t.Context(), userAuthority(t, "admin", true), config.Agent{ID: "same", Name: "Same", Scope: config.AgentScopeSystem})
			results <- a.ID
			errs <- err
		}()
	}
	close(start)
	ids := []string{<-results, <-results}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	if fmt.Sprint(ids) != "[same same-2]" {
		t.Fatalf("IDs = %v", ids)
	}
}
