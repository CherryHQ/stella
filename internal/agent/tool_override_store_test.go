package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
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
		Sandbox: json.RawMessage(`{}`), EnabledBuiltinSkills: json.RawMessage(`[]`),
		Scope: "system", Enabled: true,
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
