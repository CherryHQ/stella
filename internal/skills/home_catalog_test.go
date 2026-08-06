package skills

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type catalogInventory []HomeCatalogRoot

func (i catalogInventory) ListRoots(context.Context) ([]HomeCatalogRoot, error) { return i, nil }

type callbackWrappingHomeCatalog struct {
	inner HomeCatalogFilesystem
	wrap  func(sandbox.Filesystem) sandbox.Filesystem
}

func (w callbackWrappingHomeCatalog) UseExistingSkillFilesystem(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) (bool, error) {
	return w.inner.UseExistingSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		return use(w.wrap(filesystem))
	})
}

type growOnFirstReadFilesystem struct {
	sandbox.Filesystem
	triggerAfter    int
	successfulReads int
	grown           bool
	grow            func() error
}

func (f *growOnFirstReadFilesystem) InspectManagedSkillTarget(ctx context.Context, name string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, name)
}

func (f *growOnFirstReadFilesystem) Read(ctx context.Context, name string, options sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	reader, info, err := f.Filesystem.Read(ctx, name, options)
	if err == nil {
		f.successfulReads++
	}
	if err == nil && !f.grown && f.successfulReads > f.triggerAfter {
		f.grown = true
		if growErr := f.grow(); growErr != nil {
			_ = reader.Close()
			return nil, sandbox.FileInfo{}, growErr
		}
	}
	return reader, info, err
}

func catalogBody(name string) string {
	return "---\nname: " + name + "\ndescription: " + name + " description\n---\nbody"
}

func TestHomeCatalogPrecedenceDisabledAndDeprecated(t *testing.T) {
	db, ctx, _, migration := newSkillMigrationFixture(t)
	userID, agentID := seedFixtures(t, db)
	if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	rows := []Skill{
		{ID: "s", Scope: "system", Name: "same", Metadata: []byte(`{"created_by":"system"}`)},
		{ID: "sa", Scope: "system_agent", AgentID: agentID, Name: "same", Metadata: []byte(`{}`)},
		{ID: "u", Scope: "user", UserID: userID, Name: "same", Metadata: []byte(`{}`)},
		{ID: "ua", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "same", DisableModelInvocation: true, Metadata: []byte(`{"created_by":"reflect"}`)},
		{ID: "old", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "old", Status: SkillStatusDeprecated, Metadata: []byte(`{}`)},
		{ID: "old-system", Scope: "system", Name: "old", Metadata: []byte(`{}`)},
	}
	for _, row := range rows {
		if _, err := store.CreateManagedSkill(ctx, row, map[string]string{MainFile: catalogBody(row.Name)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewHomeCatalog(migration.homes, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Skill.Name == "same" {
			t.Fatal("disabled winner fell back to a lower scope")
		}
	}
	old, err := catalog.Resolve(ctx, "old", ViewContext{UserID: userID, AgentID: agentID})
	if err != nil || old == nil || old.Skill.Scope != "system" {
		t.Fatalf("deprecated fallback = %+v, %v", old, err)
	}
	byScope, err := catalog.ListByScope(ctx, "user_agent", userID, agentID)
	if err != nil || len(byScope) != 1 {
		t.Fatalf("scope rows = %+v, %v", byScope, err)
	}
	var disabled HomeCatalogSkill
	for _, item := range byScope {
		if item.Skill.Name == "same" {
			disabled = item
		}
	}
	if disabled.Digest == "" || !disabled.Managed || disabled.Skill.Version != 1 || string(disabled.Skill.Metadata) != `{"created_by":"manual"}` {
		t.Fatalf("descriptor lost metadata: %+v", disabled)
	}
}

func TestHomeCatalogFilesAndInventory(t *testing.T) {
	db, ctx, _, migration := newSkillMigrationFixture(t)
	userID, agentID := seedFixtures(t, db)
	rows := []Skill{{ID: "system", Scope: "system", Name: "system", Metadata: []byte(`{}`)}, {ID: "user", Scope: "user", UserID: userID, Name: "personal", Metadata: []byte(`{}`)}, {ID: "ua", Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "agent", Metadata: []byte(`{}`)}}
	if _, err := sqlc.New(db).CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	for _, row := range rows {
		if _, err := store.CreateManagedSkill(ctx, row, map[string]string{MainFile: catalogBody(row.Name), "references/blob": "bytes"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := NewHomeCatalog(migration.homes, catalogInventory{{Scope: "system"}, {Scope: "system"}, {Scope: "user", UserID: userID}, {Scope: "user_agent", UserID: userID, AgentID: agentID}})
	all, err := catalog.ListAll(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if _, err := (&HomeCatalog{homes: migration.homes}).ListAll(ctx); err == nil {
		t.Fatal("nil inventory accepted")
	}
	item, err := catalog.Resolve(ctx, "agent", ViewContext{UserID: userID, AgentID: agentID})
	if err != nil || item == nil {
		t.Fatal(err)
	}
	paths, err := catalog.ListFiles(ctx, item.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != "SKILL.md" || paths[1] != "references/blob" {
		t.Fatalf("paths = %q", paths)
	}
	content, err := catalog.LoadFile(ctx, item.Skill.ID, "references/blob")
	if err != nil || content != "bytes" {
		t.Fatalf("content = %q, %v", content, err)
	}
	files, err := catalog.ListFilesWithContent(ctx, item.Skill.ID)
	if err != nil || files["references/blob"] != "bytes" {
		t.Fatalf("files = %#v, %v", files, err)
	}
	for _, bad := range []string{"", "nope", item.Skill.ID + "x"} {
		if _, err := catalog.LoadFile(ctx, bad, MainFile); err == nil {
			t.Fatalf("bad ID %q accepted", bad)
		}
	}
	if _, err := catalog.LoadFile(ctx, item.Skill.ID, "../escape"); err == nil {
		t.Fatal("unsafe path accepted")
	}
	missing, err := home.UserSkillCatalog("absent-user")
	if err != nil {
		t.Fatal(err)
	}
	exists, err := migration.homes.UseExistingSkillFilesystem(ctx, missing, func(sandbox.Filesystem) error { return errors.New("opened missing") })
	if err != nil || exists {
		t.Fatalf("missing Home = %v, %v", exists, err)
	}
	if _, err := catalog.ListByScope(ctx, "user", "absent-user", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadFile(ctx, item.Skill.ID, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file = %v", err)
	}
}

func TestHomeCatalogListFilesWithContentRejectsOversizedOrdinaryTree(t *testing.T) {
	_, ctx, _, migration := newSkillMigrationFixture(t)
	if err := migration.homes.UseSkillFilesystem(ctx, home.SystemSkillCatalog(), func(filesystem sandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, sandbox.PathWorkspace+"/oversized", 0o755); err != nil {
			return err
		}
		if err := filesystem.Write(ctx, sandbox.PathWorkspace+"/oversized/SKILL.md", strings.NewReader(catalogBody("oversized")), sandbox.WriteOptions{}); err != nil {
			return err
		}
		body := strings.Repeat("x", 7<<20)
		for i := range 5 { // 35 MiB total; each file remains below the 8 MiB ceiling.
			if err := filesystem.Write(ctx, sandbox.PathWorkspace+"/oversized/f"+string(rune('0'+i)), strings.NewReader(body), sandbox.WriteOptions{}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewHomeCatalog(migration.homes, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(ctx, ViewContext{})
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %+v, %v", items, err)
	}
	if _, err := catalog.ListFilesWithContent(ctx, items[0].Skill.ID); err == nil || !strings.Contains(err.Error(), "content limit") {
		t.Fatalf("oversized ordinary tree = %v", err)
	}
}

func TestHomeCatalogListFilesWithContentRejectsTreeGrowthDuringRead(t *testing.T) {
	_, ctx, _, migration := newSkillMigrationFixture(t)
	const directory = sandbox.PathWorkspace + "/growth"
	if err := migration.homes.UseSkillFilesystem(ctx, home.SystemSkillCatalog(), func(filesystem sandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, directory, 0o755); err != nil {
			return err
		}
		if err := filesystem.Write(ctx, directory+"/SKILL.md", strings.NewReader(catalogBody("growth")), sandbox.WriteOptions{}); err != nil {
			return err
		}
		body := strings.Repeat("x", 6<<20)
		for i := range 5 { // 30 MiB plus SKILL.md, initially below the tree ceiling.
			if err := filesystem.Write(ctx, directory+"/f"+string(rune('0'+i)), strings.NewReader(body), sandbox.WriteOptions{}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		return &growOnFirstReadFilesystem{Filesystem: filesystem, triggerAfter: 1, grow: func() error {
			return filesystem.Write(ctx, directory+"/f4", strings.NewReader(strings.Repeat("y", 8<<20)), sandbox.WriteOptions{})
		}}
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(ctx, ViewContext{})
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %+v, %v", items, err)
	}
	files, err := catalog.ListFilesWithContent(ctx, items[0].Skill.ID)
	if !errors.Is(err, sandbox.ErrReadLimit) || files != nil {
		t.Fatalf("growth result = %#v, %v", files, err)
	}
}
