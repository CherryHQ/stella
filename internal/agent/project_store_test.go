package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestProjectStoreResolveAndEnsure(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)

	// Isolate the on-disk workspace this test creates under Ensure.
	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "projects@test.local", Name: "Projects"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agentID := "project-store-agent"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: agentID, Name: "Projects Agent", Workspace: "/tmp/projects-agent",
		Sandbox: json.RawMessage(`{}`), EnabledBuiltinSkills: json.RawMessage(`[]`),
		Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	ps := NewProjectStore(db, store.NewDBStore(db))

	// Resolve returns the base_dir of an existing project.
	var resolve ProjectResolverFunc = ps.Resolve
	created, err := q.CreateProject(ctx, sqlc.CreateProjectParams{
		ID: uuid.Must(uuid.NewV7()).String(), AgentID: agentID, UserID: user.ID,
		Name: "explicit", BaseDir: "/tmp/projects-agent/base",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	baseDir, err := resolve(ctx, created.ID, user.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if baseDir != "/tmp/projects-agent/base" {
		t.Fatalf("Resolve baseDir = %q, want /tmp/projects-agent/base", baseDir)
	}

	// Ensure returns an existing project for the pair rather than creating a new one.
	var ensure ProjectEnsurerFunc = ps.Ensure
	got, err := ensure(ctx, agentID, user.ID)
	if err != nil {
		t.Fatalf("Ensure (existing): %v", err)
	}
	if got != created.ID {
		t.Fatalf("Ensure returned %q, want existing project %q", got, created.ID)
	}

	// Ensure creates a default project when the pair has none.
	freshAgent := "project-store-agent-2"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: freshAgent, Name: "Fresh Agent", Workspace: "/tmp/fresh-agent",
		Sandbox: json.RawMessage(`{}`), EnabledBuiltinSkills: json.RawMessage(`[]`),
		Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent fresh: %v", err)
	}
	newID, err := ensure(ctx, freshAgent, user.ID)
	if err != nil {
		t.Fatalf("Ensure (create): %v", err)
	}
	if newID == "" {
		t.Fatalf("Ensure (create) returned empty project ID")
	}
	// A second call is idempotent: same project comes back.
	again, err := ensure(ctx, freshAgent, user.ID)
	if err != nil {
		t.Fatalf("Ensure (idempotent): %v", err)
	}
	if again != newID {
		t.Fatalf("Ensure not idempotent: %q != %q", again, newID)
	}
	// The created project's base_dir resolves to the agent's private area.
	wantBase := UserAgentDir(config.StellaHome(), user.ID, freshAgent)
	gotBase, err := resolve(ctx, newID, user.ID)
	if err != nil {
		t.Fatalf("Resolve (created): %v", err)
	}
	if gotBase != wantBase {
		t.Fatalf("created project base_dir = %q, want %q", gotBase, wantBase)
	}
}
