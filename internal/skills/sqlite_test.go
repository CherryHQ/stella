package skills

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

// newTestStore opens a fresh in-tmpdir SQLite DB, ensures a default org, and returns a Store.
func newTestStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultOrg: %v", err)
	}
	s := New(db)
	s.SetDefaultOrgID(orgID)
	return s, db
}

// seedFixtures inserts the auth_organization, auth_users and settings_agent rows needed by FK constraints.
// Returns (userID, agentID, orgID).
func seedFixtures(t *testing.T, db *sql.DB) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	orgID, err := appdb.EnsureDefaultOrg(ctx, db)
	if err != nil {
		t.Fatalf("ensure default org: %v", err)
	}

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
	orgCtx := config.WithOrgID(ctx, orgID)
	if err := cs.CreateAgent(orgCtx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true, OrgID: orgID,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	return u.ID, agentID, orgID
}

func TestCreateAndLoadFile(t *testing.T) {
	store, db := newTestStore(t)
	_, _ = db, db // suppress unused
	userID, agentID, _ := seedFixtures(t, db)
	ctx := context.Background()

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
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("error should wrap sql.ErrNoRows, got: %v", err)
		}
	})
}

func TestCreateMissingSkillMD(t *testing.T) {
	store, db := newTestStore(t)
	userID, _, _ := seedFixtures(t, db)
	ctx := context.Background()

	_, err := store.Create(ctx, Skill{
		Scope: "user", UserID: userID, Name: "bad", Description: "missing main",
	}, map[string]string{"only.md": "content"})

	if err == nil {
		t.Fatal("expected error when SKILL.md missing")
	}

	// Confirm no row was inserted.
	var count int
	if scanErr := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill WHERE name='bad'").Scan(&count); scanErr != nil {
		t.Fatalf("count: %v", scanErr)
	}
	if count != 0 {
		t.Errorf("skills row count = %d, want 0", count)
	}
}

func TestCreateSystemSkillSameNameAcrossOrgs(t *testing.T) {
	store1, db := newTestStore(t)
	_, _, org1 := seedFixtures(t, db)
	org2 := "org-2"
	if _, err := db.Exec(`INSERT INTO auth_organization (id, name, external_id, source) VALUES (?, ?, ?, ?)`, org2, "Org 2", org2, "test"); err != nil {
		t.Fatalf("create org 2: %v", err)
	}
	store2 := New(db)
	store2.SetDefaultOrgID(org2)
	ctx := context.Background()
	files := map[string]string{MainFile: "body"}

	id1, err := store1.Create(ctx, Skill{Scope: "system", Name: "shared", Description: "org1"}, files)
	if err != nil {
		t.Fatalf("create org1 skill: %v", err)
	}
	id2, err := store2.Create(ctx, Skill{Scope: "system", Name: "shared", Description: "org2"}, files)
	if err != nil {
		t.Fatalf("create org2 skill: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct skill IDs, got %q", id1)
	}

	got1, err := store1.Resolve(ctx, "shared", ViewContext{})
	if err != nil || got1 == nil {
		t.Fatalf("resolve org1: %v skill=%v", err, got1)
	}
	got2, err := store2.Resolve(ctx, "shared", ViewContext{})
	if err != nil || got2 == nil {
		t.Fatalf("resolve org2: %v skill=%v", err, got2)
	}
	if got1.OrgID != org1 || got2.OrgID != org2 {
		t.Fatalf("skills crossed orgs: org1=%+v org2=%+v", got1, got2)
	}
}

func TestResolvePrecedence(t *testing.T) {
	store, db := newTestStore(t)
	userID, agentID, _ := seedFixtures(t, db)
	ctx := context.Background()

	mainFile := map[string]string{MainFile: "body"}

	// Insert one skill at each scope, all named "foo".
	sysID, err := store.Create(ctx, Skill{Scope: "system", Name: "foo", Description: "sys"}, mainFile)
	if err != nil {
		t.Fatalf("create system: %v", err)
	}
	agentSkID, err := store.Create(ctx, Skill{Scope: "agent", AgentID: agentID, Name: "foo", Description: "agent"}, mainFile)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	userSkID, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, AgentID: agentID, Name: "foo", Description: "user"}, mainFile)
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

func TestVisibilityFiltering(t *testing.T) {
	store, db := newTestStore(t)
	userID, agentID, orgID := seedFixtures(t, db)
	ctx := context.Background()

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
	if err := cs.CreateAgent(config.WithOrgID(ctx, orgID), config.Agent{
		ID: "agent2", Name: "agent2", Model: "p/m", Workspace: "/tmp/agent2", Enabled: true, OrgID: orgID,
	}); err != nil {
		t.Fatalf("create agent2: %v", err)
	}

	mainFile := map[string]string{MainFile: "body"}

	t.Run("agent-scoped skill invisible to other agent", func(t *testing.T) {
		_, err := store.Create(ctx, Skill{Scope: "agent", AgentID: agentID, Name: "private-agent-skill", Description: "d"}, mainFile)
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
	store, db := newTestStore(t)
	userID, _, _ := seedFixtures(t, db)
	ctx := context.Background()

	mainFile := map[string]string{MainFile: "body"}

	t.Run("deprecated excluded from List and Resolve", func(t *testing.T) {
		id, err := store.Create(ctx, Skill{Scope: "user", UserID: userID, Name: "depr-skill", Description: "d"}, mainFile)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		status := "deprecated"
		if err := store.Update(ctx, id, ViewContext{UserID: userID}, UpdatePatch{Status: &status}); err != nil {
			t.Fatalf("Update: %v", err)
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
	store, db := newTestStore(t)
	userID, _, _ := seedFixtures(t, db)
	ctx := context.Background()

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
	store, db := newTestStore(t)
	userID, _, _ := seedFixtures(t, db)
	ctx := context.Background()

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
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill WHERE id=?", id).Scan(&skillCount); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skillCount != 0 {
		t.Errorf("skills count = %d, want 0", skillCount)
	}

	// Skill files cascade-deleted.
	var fileCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_file WHERE skill_id=?", id).Scan(&fileCount); err != nil {
		t.Fatalf("count skill_file: %v", err)
	}
	if fileCount != 0 {
		t.Errorf("skill_file count = %d, want 0", fileCount)
	}
}

// Compile-time assertion that SQLiteStore satisfies the Store interface.
var _ Store = (*SQLiteStore)(nil)
