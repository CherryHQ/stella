package skills

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// newTestStore opens a fresh isolated PostgreSQL DB and returns a Store plus a context.
func newTestStore(t *testing.T) (*PGStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	db := dbtest.New(t)
	return New(db), db, context.Background()
}

// seedFixtures inserts auth_users and agent rows needed by FK constraints.
// Returns (userID, agentID).
func seedFixtures(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()

	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "testuser@test.local",
		Name:  "testuser",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	agentID := "agent1"
	cs := cfgstore.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	return u.ID, agentID
}

func TestCreateAndLoadFile(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	sk := Skill{
		Scope:       "user",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "greet",
		Description: "says hello",
		Status:      "active",
	}
	files := map[string]string{
		MainFile:            "# Greet\nSay hello.",
		"references/api.md": "API docs here.",
	}

	id, err := store.Create(ctx, sk, files)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	t.Run("load main file", func(t *testing.T) {
		content, err := store.LoadFile(ctx, id, MainFile)
		if err != nil {
			t.Fatalf("LoadFile SKILL.md: %v", err)
		}
		if content != "# Greet\nSay hello." {
			t.Errorf("content = %q", content)
		}
	})

	t.Run("load reference file", func(t *testing.T) {
		content, err := store.LoadFile(ctx, id, "references/api.md")
		if err != nil {
			t.Fatalf("LoadFile references/api.md: %v", err)
		}
		if content != "API docs here." {
			t.Errorf("content = %q", content)
		}
	})

	t.Run("load missing file returns error", func(t *testing.T) {
		_, err := store.LoadFile(ctx, id, "nope.md")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("error should wrap pgx.ErrNoRows, got: %v", err)
		}
	})
}

func TestCreateMissingSkillMD(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	_, err := store.Create(ctx, Skill{
		Scope: "user", UserID: userID, Name: "bad", Description: "missing main",
	}, map[string]string{"only.md": "content"})

	if err == nil {
		t.Fatal("expected error when SKILL.md missing")
	}

	// Confirm no row was inserted.
	var count int
	if scanErr := db.QueryRow(ctx, "SELECT COUNT(*) FROM skill WHERE name='bad'").Scan(&count); scanErr != nil {
		t.Fatalf("count: %v", scanErr)
	}
	if count != 0 {
		t.Errorf("skills row count = %d, want 0", count)
	}
}

func TestUpdateManagedSkillReturnsSortedSnapshotFiles(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	snapshot, err := store.CreateManagedSkill(ctx, Skill{
		Scope: "user", UserID: userID, Name: "sorted-snapshot", Description: "d",
	}, map[string]string{
		MainFile:             "body",
		"scripts/run.sh":     "run",
		"references/note.md": "note",
	})
	if err != nil {
		t.Fatalf("CreateManagedSkill: %v", err)
	}

	snapshot, err = store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: snapshot.Skill.ID, Scope: "user", UserID: userID,
	})
	if err != nil {
		t.Fatalf("UpdateManagedSkill: %v", err)
	}
	want := []string{MainFile, "references/note.md", "scripts/run.sh"}
	if !reflect.DeepEqual(snapshot.Files, want) {
		t.Fatalf("snapshot files = %v, want %v", snapshot.Files, want)
	}
}

func TestResolvePrecedence(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	mainFile := map[string]string{MainFile: "body"}

	// Insert one skill at each scope, all named "foo".
	sysID, err := store.Create(ctx, Skill{Scope: "system", Name: "foo", Description: "sys"}, mainFile)
	if err != nil {
		t.Fatalf("create system: %v", err)
	}
	agentSkID, err := store.Create(ctx, Skill{Scope: "system_agent", AgentID: agentID, Name: "foo", Description: "agent"}, mainFile)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	userSkID, err := store.Create(ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "foo", Description: "user"}, mainFile)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	vc := ViewContext{UserID: userID, AgentID: agentID}

	t.Run("user wins over agent and system", func(t *testing.T) {
		sk, err := store.Resolve(ctx, "foo", vc)
		if err != nil || sk == nil {
			t.Fatalf("Resolve: %v (sk=%v)", err, sk)
		}
		if sk.ID != userSkID {
			t.Errorf("got %s (%s), want user %s", sk.ID, sk.Scope, userSkID)
		}
	})

	t.Run("agent wins after user deleted", func(t *testing.T) {
		if err := store.Delete(ctx, userSkID, ViewContext{UserID: userID, AgentID: agentID}); err != nil {
			t.Fatalf("Delete user: %v", err)
		}
		sk, err := store.Resolve(ctx, "foo", vc)
		if err != nil || sk == nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk.ID != agentSkID {
			t.Errorf("got %s (%s), want agent %s", sk.ID, sk.Scope, agentSkID)
		}
	})

	t.Run("system wins after agent deleted", func(t *testing.T) {
		if err := store.Delete(ctx, agentSkID, ViewContext{AgentID: agentID}); err != nil {
			t.Fatalf("Delete agent: %v", err)
		}
		sk, err := store.Resolve(ctx, "foo", vc)
		if err != nil || sk == nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk.ID != sysID {
			t.Errorf("got %s (%s), want system %s", sk.ID, sk.Scope, sysID)
		}
	})
}

// TestResolveFourScopePrecedence covers the full chain including the user-global
// tier introduced by #434: user_agent > user > system_agent > system.
func TestResolveFourScopePrecedence(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	mainFile := map[string]string{MainFile: "body"}

	sysID, err := store.Create(ctx, Skill{Scope: "system", Name: "foo", Description: "sys"}, mainFile)
	if err != nil {
		t.Fatalf("create system: %v", err)
	}
	sysAgentID, err := store.Create(ctx, Skill{Scope: "system_agent", AgentID: agentID, Name: "foo", Description: "system_agent"}, mainFile)
	if err != nil {
		t.Fatalf("create system_agent: %v", err)
	}
	userID2, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, Name: "foo", Description: "user"}, mainFile)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userAgentID, err := store.Create(ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "foo", Description: "user_agent"}, mainFile)
	if err != nil {
		t.Fatalf("create user_agent: %v", err)
	}

	vc := ViewContext{UserID: userID, AgentID: agentID}
	steps := []struct {
		name   string
		delete string
		dvc    ViewContext
		want   string
	}{
		{"user_agent wins", "", ViewContext{}, userAgentID},
		{"user beats system_agent", userAgentID, vc, userID2},
		{"system_agent beats system", userID2, ViewContext{UserID: userID}, sysAgentID},
		{"system last", sysAgentID, ViewContext{AgentID: agentID}, sysID},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if step.delete != "" {
				if err := store.Delete(ctx, step.delete, step.dvc); err != nil {
					t.Fatalf("Delete: %v", err)
				}
			}
			sk, err := store.Resolve(ctx, "foo", vc)
			if err != nil || sk == nil {
				t.Fatalf("Resolve: %v (sk=%v)", err, sk)
			}
			if sk.ID != step.want {
				t.Errorf("got %s (%s), want %s", sk.ID, sk.Scope, step.want)
			}
		})
	}
}

// TestScopeOwnerCheckConstraint locks in the migration's end-state invariant:
// each scope permits exactly one owner-column shape.
func TestScopeOwnerCheckConstraint(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	// Each row violates the CHECK for its scope and must be rejected.
	bad := []struct {
		name            string
		scope           string
		userID, agentID pgtype.Text
	}{
		{"user with agent", "user", pgtype.Text{String: userID, Valid: true}, pgtype.Text{String: agentID, Valid: true}},
		{"user_agent without agent", "user_agent", pgtype.Text{String: userID, Valid: true}, pgtype.Text{}},
		{"system with owner", "system", pgtype.Text{String: userID, Valid: true}, pgtype.Text{}},
		{"system_agent with user", "system_agent", pgtype.Text{String: userID, Valid: true}, pgtype.Text{String: agentID, Valid: true}},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			_, err := db.Exec(ctx,
				"INSERT INTO skill (id, scope, user_id, agent_id, name, description, status) VALUES ($1, $2, $3, $4, $5, 'd', 'active')",
				uuid.NewString(), b.scope, b.userID, b.agentID, b.name)
			if err == nil {
				t.Fatalf("expected CHECK constraint violation for %s/%s", b.scope, b.name)
			}
		})
	}
}

func TestVisibilityFiltering(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)

	// Seed a second user and agent.
	oidcStore2 := appdb.NewOIDCStore(db)
	u2, err := oidcStore2.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "user2@test.local",
		Name:  "user2",
	})
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	cs := cfgstore.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: "agent2", Name: "agent2", Model: "p/m", Workspace: "/tmp/agent2", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent2: %v", err)
	}

	mainFile := map[string]string{MainFile: "body"}

	t.Run("agent-scoped skill invisible to other agent", func(t *testing.T) {
		_, err := store.Create(ctx, Skill{Scope: "system_agent", AgentID: agentID, Name: "private-agent-skill", Description: "d"}, mainFile)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		sk, err := store.Resolve(ctx, "private-agent-skill", ViewContext{AgentID: "agent2"})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk != nil {
			t.Errorf("expected nil, got skill %s", sk.ID)
		}

		// Visible to owner.
		sk, err = store.Resolve(ctx, "private-agent-skill", ViewContext{AgentID: agentID})
		if err != nil || sk == nil {
			t.Fatalf("Resolve for owner: %v sk=%v", err, sk)
		}
	})

	t.Run("user-scoped skill invisible to other user", func(t *testing.T) {
		_, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, Name: "private-user-skill", Description: "d"}, mainFile)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		sk, err := store.Resolve(ctx, "private-user-skill", ViewContext{UserID: u2.ID})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk != nil {
			t.Errorf("expected nil for user2, got skill %s", sk.ID)
		}

		// Visible to owner.
		sk, err = store.Resolve(ctx, "private-user-skill", ViewContext{UserID: userID})
		if err != nil || sk == nil {
			t.Fatalf("Resolve for owner: %v sk=%v", err, sk)
		}
	})
}

func TestDeprecatedAndDisabled(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	mainFile := map[string]string{MainFile: "body"}

	t.Run("deprecated excluded from List and Resolve", func(t *testing.T) {
		id, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, Name: "depr-skill", Description: "d"}, mainFile)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Deprecated is a legacy read-only state; seed it below the business API.
		if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = $1`, id); err != nil {
			t.Fatalf("seed deprecated state: %v", err)
		}

		skills, err := store.List(ctx, ViewContext{UserID: userID})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, sk := range skills {
			if sk.ID == id {
				t.Error("deprecated skill should not appear in List")
			}
		}

		sk, err := store.Resolve(ctx, "depr-skill", ViewContext{UserID: userID})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk != nil {
			t.Error("deprecated skill should not appear in Resolve")
		}
		// LoadFile still works for deprecated.
		_, err = store.LoadFile(ctx, id, MainFile)
		if err != nil {
			t.Errorf("LoadFile on deprecated should still work: %v", err)
		}
	})

	t.Run("disabled excluded from List and Resolve", func(t *testing.T) {
		id, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, Name: "disabled-skill", Description: "d"}, mainFile)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		disabled := true
		if err := store.Update(ctx, id, ViewContext{UserID: userID}, UpdatePatch{DisableModelInvocation: &disabled}); err != nil {
			t.Fatalf("Update: %v", err)
		}

		skills, err := store.List(ctx, ViewContext{UserID: userID})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, sk := range skills {
			if sk.ID == id {
				t.Error("disabled skill should not appear in List")
			}
		}

		sk, err := store.Resolve(ctx, "disabled-skill", ViewContext{UserID: userID})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sk != nil {
			t.Error("disabled skill should not appear in Resolve")
		}

		// LoadFile still works.
		_, err = store.LoadFile(ctx, id, MainFile)
		if err != nil {
			t.Errorf("LoadFile on disabled should still work: %v", err)
		}
	})
}

func TestUpsertFileAndDeleteFile(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	id, err := store.Create(ctx, Skill{
		Scope: "user", UserID: userID, Name: "multi-file", Description: "d",
	}, map[string]string{
		MainFile:            "original body",
		"references/ref.md": "ref content",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("upsert modifies file", func(t *testing.T) {
		if err := store.UpsertFile(ctx, id, MainFile, "updated body"); err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}
		content, err := store.LoadFile(ctx, id, MainFile)
		if err != nil {
			t.Fatalf("LoadFile: %v", err)
		}
		if content != "updated body" {
			t.Errorf("content = %q", content)
		}
	})

	t.Run("delete reference file", func(t *testing.T) {
		if err := store.DeleteFile(ctx, id, "references/ref.md"); err != nil {
			t.Fatalf("DeleteFile: %v", err)
		}
		_, err := store.LoadFile(ctx, id, "references/ref.md")
		if err == nil {
			t.Fatal("expected error after DeleteFile")
		}

		// SKILL.md still present.
		if _, err := store.LoadFile(ctx, id, MainFile); err != nil {
			t.Errorf("SKILL.md should still exist: %v", err)
		}
	})
}

func TestDeleteCascadesSkillFiles(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	id, err := store.Create(ctx, Skill{
		Scope: "user", UserID: userID, Name: "cascade-test", Description: "d",
	}, map[string]string{
		MainFile:   "body",
		"extra.md": "extra",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Delete(ctx, id, ViewContext{UserID: userID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Skill row gone.
	var skillCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM skill WHERE id=$1", id).Scan(&skillCount); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skillCount != 0 {
		t.Errorf("skills count = %d, want 0", skillCount)
	}

	// Skill files cascade-deleted.
	var fileCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM skill_file WHERE skill_id=$1", id).Scan(&fileCount); err != nil {
		t.Fatalf("count skill_file: %v", err)
	}
	if fileCount != 0 {
		t.Errorf("skill_file count = %d, want 0", fileCount)
	}
}

func TestSkillFileBinaryContentRoundTrip(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	// NUL byte plus invalid UTF-8 sequences — rejected outright by a text column.
	binary := string([]byte{0x89, 'P', 'N', 'G', 0x00, 0xFF, 0xFE, 0x01})
	snapshot, err := store.CreateManagedSkill(ctx, Skill{
		Scope: "user", UserID: userID, Name: "binary-skill", Description: "d",
	}, map[string]string{
		MainFile:          "---\nname: binary-skill\ndescription: d\n---\n",
		"assets/logo.png": binary,
	})
	if err != nil {
		t.Fatalf("CreateManagedSkill with binary file: %v", err)
	}

	got, err := store.LoadFile(ctx, snapshot.Skill.ID, "assets/logo.png")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got != binary {
		t.Fatalf("LoadFile content = %q, want %q", got, binary)
	}

	all, err := store.ListFilesWithContent(ctx, snapshot.Skill.ID)
	if err != nil {
		t.Fatalf("ListFilesWithContent: %v", err)
	}
	if all["assets/logo.png"] != binary {
		t.Fatalf("ListFilesWithContent content = %q, want %q", all["assets/logo.png"], binary)
	}
}

// Compile-time assertion that PGStore satisfies the Store interface.
var _ Store = (*PGStore)(nil)
