package skills

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/store"
)

// newTestStore is a Home-authoritative test fixture. Legacy migration tests
// seed source rows directly with seedLegacySkill rather than reviving a SQL Store.
func newTestStore(t *testing.T) (*HomeAuthorityStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	db := dbtest.New(t)
	ctx := context.Background()
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewHomeCatalog(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewHomeSkillPublisher(registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	homeStore, err := NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}
	reflectStore, err := NewHomeReflectStore(homeStore, usage)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewHomeAuthorityStore(homeStore, reflectStore)
	if err != nil {
		t.Fatal(err)
	}
	return authority, db, ctx
}

func seedFixtures(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	u, err := appdb.NewOIDCStore(db).CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: uuid.NewString() + "@test.local", Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent1"
	if err := store.NewDBStore(db).CreateAgent(ctx, config.Agent{ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/agent1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return u.ID, agentID
}

func seedLegacySkill(t *testing.T, db *pgxpool.Pool, sk Skill, files map[string]string) {
	t.Helper()
	if sk.Status == "" {
		sk.Status = SkillStatusActive
	}
	if len(sk.Metadata) == 0 {
		sk.Metadata = []byte(`{}`)
	}
	if sk.ID == "" {
		sk.ID = uuid.NewString()
	}
	_, err := db.Exec(context.Background(), `INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, sk.ID, sk.Scope, pgtype.Text{String: sk.UserID, Valid: sk.UserID != ""}, pgtype.Text{String: sk.AgentID, Valid: sk.AgentID != ""}, sk.Name, sk.Description, sk.Status, sk.DisableModelInvocation, sk.Metadata)
	if err != nil {
		t.Fatalf("seed legacy Skill %q: %v", sk.Name, err)
	}
	for name, content := range files {
		if _, err := db.Exec(context.Background(), `INSERT INTO skill_file (skill_id,path,content) VALUES ($1,$2,$3)`, sk.ID, name, []byte(content)); err != nil {
			t.Fatalf("seed legacy Skill file %q: %v", name, err)
		}
	}
}
