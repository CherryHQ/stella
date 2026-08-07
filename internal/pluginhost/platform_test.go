package pluginhost

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/skills"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestSkillStoreAdapterPreservesContentDigest(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	pluginSkill := skillToPlugin(skills.Skill{ID: "home-skill", Version: 7, ContentDigest: digest})
	if pluginSkill.ContentDigest != digest || pluginSkill.Version != 7 {
		t.Fatalf("skillToPlugin = %+v, want digest %q and unchanged lifecycle version", pluginSkill, digest)
	}

	internalSkill := skillFromPlugin(pluginSkill)
	if internalSkill.ContentDigest != digest || internalSkill.Version != 7 {
		t.Fatalf("skillFromPlugin = %+v, want digest %q and unchanged lifecycle version", internalSkill, digest)
	}
}

func TestSkillStoreAdapterTouchesReflectSkillUsage(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedSkillAdapterFixtures(t, db)
	store := newHomeAuthorityStore(t, db)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
		UserID:          userID,
		AgentID:         agentID,
		Name:            "reflect-adapter-touch",
		Description:     "created by reflect",
		MainFileContent: "---\nname: reflect-adapter-touch\ndescription: created by reflect\n---\n# Reflect Adapter Touch\n",
	})
	if err != nil {
		t.Fatalf("CreateReflectOwnedUserAgentSkill: %v", err)
	}

	usage, err := skills.NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}
	before, err := usage.Get(ctx, skills.HomeSkillUsageIdentity{ID: created.ID, UserID: userID, AgentID: agentID, Name: created.Name, LastContentDigest: created.ContentDigest})
	if err != nil {
		t.Fatalf("read initial logical skill usage: %v", err)
	}
	tracker, ok := NewSkillStoreAdapter(store).(interface {
		TouchReflectSkillRuntimeUse(context.Context, string, string, string, string) error
	})
	if !ok {
		t.Fatal("skill store adapter does not expose runtime usage touch")
	}
	if err := tracker.TouchReflectSkillRuntimeUse(ctx, created.ID, userID, agentID, created.ContentDigest); err != nil {
		t.Fatalf("TouchReflectSkillRuntimeUse: %v", err)
	}

	after, err := usage.Get(ctx, skills.HomeSkillUsageIdentity{ID: created.ID, UserID: userID, AgentID: agentID, Name: created.Name, LastContentDigest: created.ContentDigest})
	if err != nil {
		t.Fatalf("read touched logical skill usage: %v", err)
	}
	if after.UseCount != before.UseCount+1 {
		t.Fatalf("use_count after touch = %d, want %d", after.UseCount, before.UseCount+1)
	}
}

func newHomeAuthorityStore(t *testing.T, db *pgxpool.Pool) *skills.HomeAuthorityStore {
	t.Helper()
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.NewHomeCatalog(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := skills.NewHomeSkillPublisher(registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := skills.NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	homeStore, err := skills.NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := skills.NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}
	reflectStore, err := skills.NewHomeReflectStore(homeStore, usage)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := skills.NewHomeAuthorityStore(homeStore, reflectStore)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func seedSkillAdapterFixtures(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "skill-adapter@test.local",
		Name:  "skill-adapter",
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
