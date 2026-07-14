package access

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

func TestAgentAccessMatrix(t *testing.T) {
	ctx := context.Background()
	store := testStore{
		agents: map[string]config.Agent{
			"system":    {ID: "system", Scope: config.AgentScopeSystem, CreatorID: "owner", Enabled: true},
			"assigned":  {ID: "assigned", Scope: config.AgentScopeRestricted, CreatorID: "owner", Enabled: true},
			"owned":     {ID: "owned", Scope: config.AgentScopeRestricted, CreatorID: "owner", Enabled: true},
			"dedicated": {ID: "dedicated", Scope: config.AgentScopeRestricted, CreatorID: "owner", Enabled: true},
		},
		channels: map[string]config.Channel{
			"channel-1": {ID: "channel-1", AgentID: "dedicated"},
		},
	}
	assign := &testAssignments{ids: []string{"assigned"}}
	access := NewService(store, assign)
	owner := userAuthority(t, "owner", false)
	other := userAuthority(t, "other", false)
	admin := userAuthority(t, "admin", true)

	for _, tc := range []struct {
		name      string
		authority authz.Authority
		action    authz.Action
		agentID   string
		wantErr   error
	}{
		{"admin manages every valid agent", admin, authz.ActionManage, "assigned", nil},
		{"admin deletes every valid agent", admin, authz.ActionDelete, "assigned", nil},
		{"user reads system", owner, authz.ActionRead, "system", nil},
		{"user executes system", owner, authz.ActionExecute, "system", nil},
		{"user reads assignment", owner, authz.ActionRead, "assigned", nil},
		{"user executes assignment", owner, authz.ActionExecute, "assigned", nil},
		{"user cannot read another private agent", other, authz.ActionRead, "owned", ErrForbidden},
		{"creator manages own agent", owner, authz.ActionManage, "owned", nil},
		{"creator deletes own agent", owner, authz.ActionDelete, "owned", nil},
		{"other user cannot manage agent", other, authz.ActionManage, "owned", ErrForbidden},
		{"delegated exact executor reads", mustWorkerAuthority(t, "owner", "owned"), authz.ActionRead, "owned", nil},
		{"delegated exact executor executes", mustWorkerAuthority(t, "owner", "owned"), authz.ActionExecute, "owned", nil},
		{"delegated actor cannot switch executor", mustWorkerAuthority(t, "owner", "system"), authz.ActionExecute, "owned", ErrForbidden},
		{"delegated actor cannot manage", mustWorkerAuthority(t, "owner", "owned"), authz.ActionManage, "owned", ErrForbidden},
		{"group agent exact executor", mustGroupAuthority(t, "g1", "owned"), authz.ActionExecute, "owned", nil},
		{"group agent cannot switch executor", mustGroupAuthority(t, "g1", "system"), authz.ActionRead, "owned", ErrForbidden},
		{"system reads", mustSystemAuthority(t), authz.ActionRead, "owned", nil},
		{"system executes", mustSystemAuthority(t), authz.ActionExecute, "owned", nil},
		{"unknown action denies", owner, authz.ActionWrite, "owned", ErrForbidden},
		{"unknown action denies admin", admin, authz.ActionWrite, "owned", ErrForbidden},
		{"missing agent remains not found", owner, authz.ActionRead, "missing", ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := access.Authorize(ctx, tc.authority, tc.agentID, tc.action)
			if tc.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}

	if err := access.CanList(ctx, owner); err != nil {
		t.Fatalf("user list = %v", err)
	}
	if err := access.CanCreate(ctx, owner); err != nil {
		t.Fatalf("user create = %v", err)
	}
	if err := access.CanList(ctx, mustWorkerAuthority(t, "owner", "owned")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegated list = %v", err)
	}
	if err := access.CanCreate(ctx, mustWorkerAuthority(t, "owner", "owned")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegated create = %v", err)
	}
}

func TestDedicatedChannelUseRequiresExactBindingAndCurrentBinding(t *testing.T) {
	ctx := context.Background()
	authority, err := authz.NewChannelAuthority("user", false, "channel-1")
	if err != nil {
		t.Fatal(err)
	}
	store := testStore{
		agents:   map[string]config.Agent{"dedicated": {ID: "dedicated", Scope: config.AgentScopeRestricted, CreatorID: "owner", Enabled: true}},
		channels: map[string]config.Channel{"channel-1": {ID: "channel-1", AgentID: "dedicated"}},
	}
	access := NewService(store, &testAssignments{})
	// The plain Use path never infers a dedicated channel binding: a held binding
	// alone is not authority for an arbitrary agent.
	if _, err := access.Use(ctx, authority, "dedicated"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bare channel binding must not authorize use: %v", err)
	}
	bound, err := access.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.UseDedicated(ctx, "dedicated", "channel-1"); err != nil {
		t.Fatalf("dedicated use = %v", err)
	}
	bound, err = access.Begin(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.UseDedicated(ctx, "other", "channel-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mismatched binding = %v", err)
	}
	// A delegated agent has no channel binding to hold, so the dedicated path is
	// closed to it.
	delegated, err := authz.NewAgentAuthority("user", "dedicated")
	if err != nil {
		t.Fatal(err)
	}
	bound, err = access.Begin(ctx, delegated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.UseDedicated(ctx, "dedicated", "channel-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegated channel use = %v", err)
	}
	if _, err := access.Begin(ctx, authz.Authority{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid authority = %v", err)
	}
}

func TestListFilteringAndAssignmentFailureFailClosed(t *testing.T) {
	ctx := context.Background()
	store := testStore{agents: map[string]config.Agent{
		"system":   {ID: "system", Scope: config.AgentScopeSystem, Enabled: true},
		"assigned": {ID: "assigned", Scope: config.AgentScopeRestricted, Enabled: true},
		"hidden":   {ID: "hidden", Scope: config.AgentScopeRestricted, Enabled: true},
	}}
	visible, err := NewService(store, &testAssignments{ids: []string{"assigned"}}).ListReadable(ctx, userAuthority(t, "u1", false), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible = %#v, want system and assigned", visible)
	}
	if _, err := NewService(store, &testAssignments{err: errors.New("db")}).ListReadable(ctx, userAuthority(t, "u1", false), false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("assignment failure = %v", err)
	}
}

func mustWorkerAuthority(t *testing.T, owner, executor string) authz.Authority {
	t.Helper()
	a, err := WorkerAgentAuthority(owner, executor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustSystemAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := SystemAgentAuthority("test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustGroupAuthority(t *testing.T, group, executor string) authz.Authority {
	t.Helper()
	a, err := GroupAgentAuthority(group, executor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
