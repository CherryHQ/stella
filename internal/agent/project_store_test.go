package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type projectWorkspaceViewer struct {
	view        home.WorkspaceView
	err         error
	expectedCtx context.Context
	contextOK   []bool
}

func (v *projectWorkspaceViewer) WorkspaceView(ctx context.Context, _ home.WorkspaceRequest) (home.WorkspaceView, error) {
	if v.expectedCtx != nil {
		v.contextOK = append(v.contextOK, ctx.Value(projectContextKey{}) == v.expectedCtx.Value(projectContextKey{}))
	}
	return v.view, v.err
}

func (v *projectWorkspaceViewer) OpenRoot(context.Context, home.WorkspaceRequest, home.RootScope, home.RootAccess) (home.RootOperations, error) {
	return nil, v.err
}

type projectContextKey struct{}

func TestProjectStoreResolve(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "projects@test.local", Name: "Projects"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agentID := "project-store-agent"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: agentID, Name: "Projects Agent", Workspace: "/tmp/projects-agent",
		Sandbox: json.RawMessage(`{}`),
		Scope:   "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	ps := NewProjectStore(db, nil)

	// Resolve converts a logical persisted path only at the trusted runtime boundary.
	var resolve ProjectResolverFunc = ps.Resolve
	created, err := q.CreateProject(ctx, sqlc.CreateProjectParams{
		ID: uuid.Must(uuid.NewV7()).String(), AgentID: agentID, UserID: user.ID,
		Name: "explicit", BaseDir: "base",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	descriptor, err := resolve(ctx, created.ID, user.ID, agentID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if descriptor.Path != "base" {
		t.Fatalf("Resolve path = %q, want base", descriptor.Path)
	}
}

func TestProjectStoreIsolatesUnresolvedCoordinates(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)
	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "isolated-projects@test.local"})
	if err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"mixed-projects", "unresolved-only"} {
		if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: agentID, Name: agentID, Workspace: "", Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	// Reproduce a database waiting for background coordinate reconciliation.
	if _, err := db.Exec(ctx, "ALTER TABLE project DROP CONSTRAINT project_base_dir_canonical_check"); err != nil {
		t.Fatal(err)
	}
	badID, goodID, onlyBadID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `
INSERT INTO project(id,agent_id,user_id,name,base_dir) VALUES
($1,'mixed-projects',$4,'legacy','/legacy/outside'),
($2,'mixed-projects',$4,'usable','repos/usable'),
($3,'unresolved-only',$4,'legacy-only','/legacy/only')`, badID, goodID, onlyBadID, user.ID); err != nil {
		t.Fatal(err)
	}
	ps := NewProjectStore(db, &fakeProjectAuth{}, WithProjectHomeWorkspace(&projectWorkspaceViewer{}))
	authority := projectUserAuthority(t, user.ID)

	projects, err := ps.List(ctx, authority, "mixed-projects", false)
	if err != nil || len(projects) != 2 {
		t.Fatalf("List with unresolved row = %#v, %v", projects, err)
	}
	byID := map[string]Project{projects[0].ID: projects[0], projects[1].ID: projects[1]}
	if !byID[badID].IsUnavailable || byID[badID].BaseDir != "" || byID[goodID].IsUnavailable {
		t.Fatalf("List availability = %#v", byID)
	}
	if project, err := ps.Get(ctx, authority, "mixed-projects", badID); err != nil || !project.IsUnavailable || project.BaseDir != "" {
		t.Fatalf("Get unresolved row = %#v, %v", project, err)
	}
	repairedBase := "repos/repaired"
	if repaired, err := ps.Update(ctx, authority, "mixed-projects", badID, ProjectUpdate{BaseDir: &repairedBase}); err != nil || repaired.IsUnavailable || repaired.BaseDir != repairedBase {
		t.Fatalf("repair unresolved row = %#v, %v", repaired, err)
	}
	if err := ps.Delete(ctx, authority, "unresolved-only", onlyBadID); err != nil {
		t.Fatalf("delete unresolved row: %v", err)
	}
}

func TestProjectStoreCreateAndUpdateLogicalPathsNeedNoFilesystem(t *testing.T) {
	ctx := context.WithValue(context.Background(), projectContextKey{}, "marker")
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)
	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "workspace-error@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	const agentID = "workspace-error-agent"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: agentID, Name: "Workspace Error", Workspace: "/tmp/ignored", Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	viewer := &projectWorkspaceViewer{expectedCtx: ctx, err: errors.New("home unavailable")}
	ps := NewProjectStore(db, &fakeProjectAuth{}, WithProjectHomeWorkspace(viewer))
	authority := projectUserAuthority(t, user.ID)

	createdProject, err := ps.Create(ctx, authority, agentID, "new", "anywhere", nil)
	if err != nil || createdProject.BaseDir != "anywhere" {
		t.Fatalf("Create = %+v, %v", createdProject, err)
	}
	created, err := q.CreateProject(ctx, sqlc.CreateProjectParams{ID: uuid.NewString(), AgentID: agentID, UserID: user.ID, Name: "existing", BaseDir: "old"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	baseDir := "new"
	updated, err := ps.Update(ctx, authority, agentID, created.ID, ProjectUpdate{BaseDir: &baseDir})
	if err != nil || updated.BaseDir != "new" {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	if len(viewer.contextOK) != 0 {
		t.Fatalf("logical metadata mutation touched filesystem %v", viewer.contextOK)
	}
}

func TestProjectStoreCanonicalizesLogicalCoordinates(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)
	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "project-coordinates@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	const agentID = "project-coordinate-agent"
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: agentID, Name: "Coordinates", Workspace: "", Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ps := NewProjectStore(db, &fakeProjectAuth{}, WithProjectHomeWorkspace(&projectWorkspaceViewer{}))
	authority := projectUserAuthority(t, user.ID)
	var updateProjectID string
	for i, tc := range []struct {
		value, want string
	}{
		{"", "."},
		{".", "."},
		{"$HOME", "."},
		{"/workspace", "."},
		{"$HOME/projects/app", "projects/app"},
		{"/workspace/projects/app", "projects/app"},
	} {
		created, err := ps.Create(ctx, authority, agentID, fmt.Sprintf("coordinate-%d", i), tc.value, nil)
		if err != nil {
			t.Fatalf("Create(%q): %v", tc.value, err)
		}
		if created.BaseDir != tc.want {
			t.Fatalf("Create(%q) BaseDir=%q, want %q", tc.value, created.BaseDir, tc.want)
		}
		row, err := q.GetProject(ctx, sqlc.GetProjectParams{ID: created.ID, UserID: user.ID})
		if err != nil || row.BaseDir != tc.want {
			t.Fatalf("stored Create(%q)=%q err=%v", tc.value, row.BaseDir, err)
		}
		if updateProjectID == "" {
			updateProjectID = created.ID
		}
		updated, err := ps.Update(ctx, authority, agentID, updateProjectID, ProjectUpdate{BaseDir: &tc.value})
		if err != nil || updated.BaseDir != tc.want {
			t.Fatalf("Update(%q)=%q err=%v", tc.value, updated.BaseDir, err)
		}
		updatedRow, err := q.GetProject(ctx, sqlc.GetProjectParams{ID: updateProjectID, UserID: user.ID})
		if err != nil || updatedRow.BaseDir != tc.want {
			t.Fatalf("stored Update(%q)=%q err=%v", tc.value, updatedRow.BaseDir, err)
		}
		persisted, err := q.CreateProject(ctx, sqlc.CreateProjectParams{ID: uuid.NewString(), AgentID: agentID, UserID: user.ID, Name: fmt.Sprintf("persisted-%d", i), BaseDir: tc.want})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ps.projectFromRow(ctx, persisted)
		if err != nil || decoded.BaseDir != tc.want || filepath.IsAbs(decoded.BaseDir) {
			t.Fatalf("decode persisted %q=%q err=%v", tc.want, decoded.BaseDir, err)
		}
	}
	for _, value := range []string{"/user", "$STELLA_ASSETS_DIR/x", "../escape", "/physical/outside"} {
		if _, err := ps.Create(ctx, authority, agentID, "escape", value, nil); !errors.Is(err, ErrInvalidBaseDir) {
			t.Fatalf("Create(%q) error=%v, want ErrInvalidBaseDir", value, err)
		}
	}
	for _, value := range []string{
		"C:/outside/project",
		"project:name",
		"control/\x00child",
		"control/\x1fchild",
		"control/\x7fchild",
	} {
		if _, err := ps.Create(ctx, authority, agentID, "portable-invalid", value, nil); !errors.Is(err, ErrInvalidBaseDir) {
			t.Errorf("Create(%q) error=%v, want ErrInvalidBaseDir", value, err)
		}
		if _, err := ps.Update(ctx, authority, agentID, updateProjectID, ProjectUpdate{BaseDir: &value}); !errors.Is(err, ErrInvalidBaseDir) {
			t.Errorf("Update(%q) error=%v, want ErrInvalidBaseDir", value, err)
		}
		persisted := sqlc.Project{UserID: user.ID, AgentID: agentID, BaseDir: value}
		if _, err := ps.projectFromRow(ctx, persisted); !errors.Is(err, ErrInvalidBaseDir) {
			t.Errorf("decode persisted %q error=%v, want ErrInvalidBaseDir", value, err)
		}
	}
}

type fakeProjectAuth struct {
	err   error
	calls int
}

func (f *fakeProjectAuth) Authorize(_ context.Context, _ authz.Authority, _ string, _ authz.Action) error {
	f.calls++
	return f.err
}

func projectUserAuthority(t *testing.T, userID string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestProjectStoreGateFailsClosed proves every CRUD use case authorizes agent
// access first: a denied authority returns the sentinel and never reaches the nil
// query handle (which would panic).
func TestProjectStoreGateFailsClosed(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("denied")
	fa := &fakeProjectAuth{err: denied}
	ps := NewProjectStore(nil, fa, WithProjectHomeWorkspace(testWorkspaceViewer{root: t.TempDir()})) // nil db: any query before the gate panics
	auth := projectUserAuthority(t, "u1")

	if _, err := ps.List(ctx, auth, "a", false); !errors.Is(err, denied) {
		t.Fatalf("List = %v, want denied", err)
	}
	if _, err := ps.Create(ctx, auth, "a", "n", "/x", nil); !errors.Is(err, denied) {
		t.Fatalf("Create = %v, want denied", err)
	}
	if _, err := ps.Get(ctx, auth, "a", "p"); !errors.Is(err, denied) {
		t.Fatalf("Get = %v, want denied", err)
	}
	if _, err := ps.Update(ctx, auth, "a", "p", ProjectUpdate{}); !errors.Is(err, denied) {
		t.Fatalf("Update = %v, want denied", err)
	}
	if err := ps.Delete(ctx, auth, "a", "p"); !errors.Is(err, denied) {
		t.Fatalf("Delete = %v, want denied", err)
	}
	if fa.calls != 5 {
		t.Fatalf("gate consulted %d times, want 5", fa.calls)
	}
}

// TestProjectStoreCRUDOwnershipAndTraversal exercises the durable use cases:
// base_dir traversal rejection, deterministic create, route-agent-mismatch and
// foreign-owner opacity, and delete.
func TestProjectStoreCRUDOwnershipAndTraversal(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	oidc := appdb.NewOIDCStore(db)

	t.Setenv("STELLA_HOME", t.TempDir())
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@test.local", Name: "Owner"})
	if err != nil {
		t.Fatalf("CreateUser(owner): %v", err)
	}
	other, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "other@test.local", Name: "Other"})
	if err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}
	mkAgent := func(id string) {
		if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
			ID: id, Name: id, Workspace: "/tmp/" + id,
			Sandbox: json.RawMessage(`{}`),
			Scope:   "system", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateAgent(%s): %v", id, err)
		}
	}
	agentID := "crud-agent"
	otherAgentID := "crud-other-agent"
	mkAgent(agentID)
	mkAgent(otherAgentID)

	ps := NewProjectStore(db, &fakeProjectAuth{}, WithProjectHomeWorkspace(testWorkspaceViewer{root: config.StellaHome()}))
	auth := projectUserAuthority(t, user.ID)

	// Traversal: a base_dir outside the agent workspace is rejected.
	if _, err := ps.Create(ctx, auth, agentID, "p1", "/etc/passwd", nil); !errors.Is(err, ErrInvalidBaseDir) {
		t.Fatalf("traversal create = %v, want ErrInvalidBaseDir", err)
	}

	// Valid canonical relative path under the agent's workspace.
	created, err := ps.Create(ctx, auth, agentID, "p1", "project", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.AgentID != agentID || created.UserID != user.ID {
		t.Fatalf("created = %+v", created)
	}
	if created.BaseDir != "project" {
		t.Fatalf("created base_dir = %q, want logical project", created.BaseDir)
	}

	// Get with the correct route agent succeeds.
	if got, err := ps.Get(ctx, auth, agentID, created.ID); err != nil || got.ID != created.ID {
		t.Fatalf("get = (%+v, %v)", got, err)
	}

	// Route-agent mismatch is opaque.
	if _, err := ps.Get(ctx, auth, otherAgentID, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("route mismatch get = %v, want ErrProjectNotFound", err)
	}
	// A foreign owner cannot see it.
	otherAuth := projectUserAuthority(t, other.ID)
	if _, err := ps.Get(ctx, otherAuth, agentID, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("foreign get = %v, want ErrProjectNotFound", err)
	}
	// Update/Delete under the wrong route agent are opaque too.
	if _, err := ps.Update(ctx, auth, otherAgentID, created.ID, ProjectUpdate{}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("route mismatch update = %v, want ErrProjectNotFound", err)
	}
	if err := ps.Delete(ctx, auth, otherAgentID, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("route mismatch delete = %v, want ErrProjectNotFound", err)
	}

	// A rename via Update persists.
	newName := "renamed"
	if updated, err := ps.Update(ctx, auth, agentID, created.ID, ProjectUpdate{Name: &newName}); err != nil || updated.Name != "renamed" {
		t.Fatalf("update = (%+v, %v)", updated, err)
	}

	// List returns the project; delete then get is opaque.
	if list, err := ps.List(ctx, auth, agentID, false); err != nil || len(list) != 1 {
		t.Fatalf("list = (%v, %v)", list, err)
	}
	if err := ps.Delete(ctx, auth, agentID, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ps.Get(ctx, auth, agentID, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("get after delete = %v, want ErrProjectNotFound", err)
	}
}
