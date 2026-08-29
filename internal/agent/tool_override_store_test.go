package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestToolOverrideStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "tools@test.local", Name: "Tools"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agentID := "tool-override-agent"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: agentID, Name: "Tools Agent", Workspace: "/tmp/tools-agent",
		Sandbox: json.RawMessage(`{}`),
		Scope:   "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	row, err := q.UpsertToolOverride(ctx, sqlc.UpsertToolOverrideParams{
		ToolName: "memory", Scope: ToolOverrideScopeUserAgent,
		UserID: pgnull.Text(user.ID), AgentID: pgnull.Text(agentID), Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpsertToolOverride: %v", err)
	}
	if row.ToolName != "memory" || row.Enabled {
		t.Fatalf("row = %+v, want disabled memory", row)
	}

	rows, err := q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(user.ID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		t.Fatalf("ListToolOverridesForAgentContext: %v", err)
	}
	if len(rows) != 1 || rows[0].ToolName != "memory" || rows[0].Scope != ToolOverrideScopeUserAgent {
		t.Fatalf("rows = %+v, want one memory user_agent override", rows)
	}

	// ToolOverrideStore.Fetch must expose the same row through the
	// ToolOverrideFetcher signature.
	var fetch ToolOverrideFetcher = NewToolOverrideStore(db).Fetch
	fetched, err := fetch(ctx, user.ID, agentID)
	if err != nil {
		t.Fatalf("ToolOverrideStore.Fetch: %v", err)
	}
	if len(fetched) != 1 || fetched[0] != (ToolOverride{ToolName: "memory", Scope: ToolOverrideScopeUserAgent, Enabled: false}) {
		t.Fatalf("fetched = %+v, want one disabled memory user_agent override", fetched)
	}

	if err := q.DeleteToolOverride(ctx, sqlc.DeleteToolOverrideParams{
		ToolName: "memory", Scope: ToolOverrideScopeUserAgent,
		UserID: pgnull.Text(user.ID), AgentID: pgnull.Text(agentID),
	}); err != nil {
		t.Fatalf("DeleteToolOverride: %v", err)
	}
	rows, err = q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(user.ID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		t.Fatalf("ListToolOverridesForAgentContext after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after delete = %+v, want none", rows)
	}
}

func TestToolOverrideStoreConditionalWrites(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := NewToolOverrideStore(db)
	key := ToolOverrideKey{ToolName: "scheduler_job_list", Scope: ToolOverrideScopeSystem}

	absent, err := store.Get(ctx, key)
	if err != nil || absent.Version != ToolOverrideAbsentVersion || absent.Present {
		t.Fatalf("absent Get = %+v, %v", absent, err)
	}
	created, err := store.SetIfVersion(ctx, ToolOverrideWrite{ToolName: key.ToolName, Scope: key.Scope, UserID: key.UserID, AgentID: key.AgentID, Enabled: false}, absent.Version)
	if err != nil || !created.Present || created.Version == ToolOverrideAbsentVersion {
		t.Fatalf("absent SetIfVersion = %+v, %v", created, err)
	}
	if _, err := store.SetIfVersion(ctx, ToolOverrideWrite{ToolName: key.ToolName, Scope: key.Scope, UserID: key.UserID, AgentID: key.AgentID, Enabled: true}, ToolOverrideAbsentVersion); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("concurrent absent insert error = %v, want conflict", err)
	}
	updated, err := store.SetIfVersion(ctx, ToolOverrideWrite{ToolName: key.ToolName, Scope: key.Scope, UserID: key.UserID, AgentID: key.AgentID, Enabled: true}, created.Version)
	if err != nil || !updated.Enabled || updated.Version == created.Version {
		t.Fatalf("existing SetIfVersion = %+v, %v", updated, err)
	}
	if err := store.ClearIfVersion(ctx, key, created.Version); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("stale ClearIfVersion error = %v, want conflict", err)
	}
	if err := store.ClearIfVersion(ctx, key, updated.Version); err != nil {
		t.Fatalf("fresh ClearIfVersion: %v", err)
	}
}

// TestToolOverrideStoreRejectsUnknownScope proves the Agent domain owns the
// scope-vocabulary invariant on write: an unrecognized scope fails before any
// query runs, so the transport can never persist an override under a bogus scope.
func TestToolOverrideStoreRejectsUnknownScope(t *testing.T) {
	s := &ToolOverrideStore{}
	if err := s.Set(context.Background(), ToolOverrideWrite{ToolName: "memory", Scope: "bogus", Enabled: true}); err == nil {
		t.Fatal("Set with unknown scope = nil, want error")
	}
	if err := s.Clear(context.Background(), ToolOverrideKey{ToolName: "memory", Scope: "bogus"}); err == nil {
		t.Fatal("Clear with unknown scope = nil, want error")
	}
}
