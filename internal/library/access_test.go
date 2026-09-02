package library

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
)

type libraryAgentStore struct {
	agents map[string]config.Agent
}

func (s libraryAgentStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	agent, ok := s.agents[id]
	if !ok {
		return config.Agent{}, pgx.ErrNoRows
	}
	return agent, nil
}

func (s libraryAgentStore) ListAgents(context.Context) ([]config.Agent, error) {
	return nil, nil
}

type libraryAssignments struct{}

func (libraryAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func libraryAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(userID), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func TestResolveManageOwnerFourScopePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agents := agentaccess.NewService(libraryAgentStore{agents: map[string]config.Agent{
		"a1": {ID: "a1", Scope: config.AgentScopeSystem},
	}}, libraryAssignments{})
	service := &Service{agentAccess: agents}
	user := libraryAuthority(t, "u1", false)
	admin := libraryAuthority(t, "admin", true)

	tests := []struct {
		name      string
		authority authz.Authority
		scope     Scope
		agentID   string
		want      Owner
		wantErr   error
	}{
		{"user", user, ScopeUser, "", Owner{Scope: ScopeUser, UserID: "u1"}, nil},
		{
			"user agent",
			user,
			ScopeUserAgent,
			"a1",
			Owner{Scope: ScopeUserAgent, UserID: "u1", AgentID: "a1"},
			nil,
		},
		{"user denied system", user, ScopeSystem, "", Owner{}, ErrForbidden},
		{"user denied system agent", user, ScopeSystemAgent, "a1", Owner{}, ErrForbidden},
		{"admin system", admin, ScopeSystem, "", Owner{Scope: ScopeSystem}, nil},
		{
			"admin system agent",
			admin,
			ScopeSystemAgent,
			"a1",
			Owner{Scope: ScopeSystemAgent, AgentID: "a1"},
			nil,
		},
		{"agent required", user, ScopeUserAgent, "", Owner{}, ErrInvalidOwner},
		{"agent forbidden", user, ScopeUser, "a1", Owner{}, ErrInvalidOwner},
		{"agent missing", user, ScopeUserAgent, "missing", Owner{}, ErrNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := service.ResolveManageOwner(ctx, test.authority, test.scope, test.agentID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveManageOwner error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("ResolveManageOwner = %+v, want %+v", got, test.want)
			}
		})
	}
}
