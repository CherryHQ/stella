package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func newHomeSkillManagerFixture(t *testing.T) (*HomeSkillManager, *HomeCatalog, context.Context, *skillMigrationService, string, string, *time.Time) {
	t.Helper()
	db, ctx, _, migration := newSkillMigrationFixture(t)
	userID, agentID := seedFixtures(t, db)
	catalog, err := NewHomeCatalog(migration.homes, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewHomeSkillPublisher(migration.homes)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	manager, err := NewHomeSkillManager(catalog, publisher, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return manager, catalog, ctx, migration, userID, agentID, &now
}

func managedSkillMarkdown(name, description, body string) []byte {
	return []byte("---\nname: " + name + "\ndescription: " + description + "\nunknown: retained\n---\n" + body)
}

func TestHomeSkillManagerCreatesAllTypedRootsAndReadsPinnedBinarySnapshot(t *testing.T) {
	manager, catalog, ctx, _, userID, agentID, _ := newHomeSkillManagerFixture(t)
	requests := []HomeSkillCreateRequest{
		{Scope: "system", Name: "system-skill", Description: "system description"},
		{Scope: "system_agent", AgentID: agentID, Name: "agent-skill", Description: "agent description"},
		{Scope: "user", UserID: userID, Name: "user-skill", Description: "user description"},
		{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "pair-skill", Description: "pair description"},
	}
	for i := range requests {
		request := requests[i]
		mode := fs.FileMode(0o755)
		request.Files = []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown(request.Name, "source description", "body")}, {Path: "bin/run", Content: []byte{0, 0xff, byte(i)}, Mode: &mode}}
		created, err := manager.Create(ctx, request)
		if err != nil {
			t.Fatalf("create %s: %v", request.Scope, err)
		}
		if created.Skill.ContentDigest == "" || created.Skill.ID == "" || created.Skill.Description != request.Description || created.Files[0] != MainFile {
			t.Fatalf("create snapshot = %+v", created)
		}
		loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
		if err != nil {
			t.Fatalf("load %s: %v", request.Scope, err)
		}
		if loaded.ContentDigest != created.Skill.ContentDigest || loaded.Skill.Scope != request.Scope || string(loaded.Files[1].Content) != string([]byte{0, 0xff, byte(i)}) || loaded.Files[1].Mode != mode {
			t.Fatalf("loaded %s = %+v", request.Scope, loaded)
		}
	}
}

func TestHomeSkillManagerUpdatePublishesOneCompleteCanonicalRevision(t *testing.T) {
	manager, catalog, ctx, _, userID, _, now := newHomeSkillManagerFixture(t)
	executable := fs.FileMode(0o755)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{
		Scope: "user", UserID: userID, Name: "updateable", Description: "before",
		Metadata: map[string]any{"created_by": ManualSkillCreatedBy, "source": "test"},
		Files:    []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("wrong-name", "wrong description", "body stays\n")}, {Path: "bin/run", Content: []byte{0, 0xff}, Mode: &executable}, {Path: "notes.txt", Content: []byte("old")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	description := "after"
	updated, err := manager.Update(ctx, HomeSkillUpdateRequest{
		ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, Description: &description,
		FileUpserts: []HomeSkillFileInput{{Path: "notes.txt", Content: []byte{0, 1, 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Skill.ContentDigest == created.Skill.ContentDigest || updated.Skill.Version != 1 || !updated.Skill.UpdatedAt.Equal(*now) {
		t.Fatalf("updated snapshot = %+v", updated)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, updated.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skill.Description != description || loaded.Skill.Version != 1 || len(loaded.Files) != 3 || loaded.Files[1].Path != "bin/run" || loaded.Files[1].Mode != executable || string(loaded.Files[1].Content) != string([]byte{0, 0xff}) {
		t.Fatalf("complete update lost an untouched file: %+v", loaded)
	}
	main := string(loaded.Files[0].Content)
	if !containsAll(main, "name: updateable", "description: after", "unknown: retained", "body stays\n") {
		t.Fatalf("frontmatter/body was not canonicalized: %q", main)
	}
	if _, err := manager.DeleteFile(ctx, updated.Skill.ID, updated.Skill.ContentDigest, MainFile); err == nil {
		t.Fatal("SKILL.md deletion succeeded")
	}
}

func TestHomeSkillManagerConflictsConversionsAndUnpublish(t *testing.T) {
	manager, catalog, ctx, migration, userID, agentID, _ := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "reflect-owned", Description: "before",
		Metadata: map[string]any{"created_by": ReflectSkillCreatedBy},
		Files:    []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("reflect-owned", "before", "body")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "reflect.txt", Content: []byte("changed")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, ConvertToManual: true}); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("stale conversion = %v", err)
	}
	current, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: current.Skill.ID, ExpectedDigest: current.ContentDigest, ConvertToManual: true})
	if err != nil {
		t.Fatal(err)
	}
	if CreatedBy(converted.Skill) != ManualSkillCreatedBy {
		t.Fatalf("conversion = %s", CreatedBy(converted.Skill))
	}
	if _, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: converted.Skill.ID, ExpectedDigest: converted.Skill.ContentDigest, ConvertToManual: true}); !errors.Is(err, ErrSkillNotReflectOwned) {
		t.Fatalf("manual conversion = %v", err)
	}
	if err := manager.Delete(ctx, converted.Skill.ID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	if err := manager.Delete(ctx, converted.Skill.ID, converted.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadManagedSnapshot(ctx, converted.Skill.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted direct selection = %v", err)
	}
	root, err := home.UserAgentSkillCatalog(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migration.homes.UseExistingSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		_, err := filesystem.Stat(ctx, path.Join(sandbox.PathWorkspace, ".stella-revisions", "reflect-owned", converted.Skill.ContentDigest, MainFile))
		return err
	}); err != nil {
		t.Fatalf("unpublish discarded retained revision: %v", err)
	}
}

func TestHomeSkillManagerConcurrentUpdatesHaveOneWinner(t *testing.T) {
	manager, catalog, ctx, _, userID, _, _ := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "contended", Description: "before", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("contended", "before", "body")}}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, value := range []string{"one", "two"} {
		wait.Go(func() {
			<-start
			_, err := manager.Update(context.Background(), HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "result", Content: []byte(value)}}})
			results <- err
		})
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrHomeSkillConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("updates success/conflict = %d/%d", successes, conflicts)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil || len(loaded.Files) != 2 {
		t.Fatalf("winner snapshot = %+v, %v", loaded, err)
	}
}

func TestHomeCatalogLoadManagedSnapshotPinsInspectedRevisionAndRejectsOrdinary(t *testing.T) {
	manager, catalog, ctx, migration, userID, _, now := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "pinned", Description: "before", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("pinned", "before", "body")}, {Path: "blob", Content: []byte{0, 0xff}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if _, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "blob", Content: []byte("new")}}}); err != nil {
		t.Fatal(err)
	}
	metadataValue, err := decodeStrictJSON(before.Skill.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata := metadataValue.(map[string]any)
	oldTree := skillTree{Metadata: skillMetadataEnvelope{Status: before.Skill.Status, DisableModelInvocation: before.Skill.DisableModelInvocation, Metadata: metadata, CreatedAt: before.Skill.CreatedAt, UpdatedAt: before.Skill.UpdatedAt, LegacyLifecycleVersion: before.Skill.Version}, Files: homeSkillTreeEntries(before.Files)}
	flipped := false
	pinnedCatalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		return &flipManagedTargetFilesystem{Filesystem: filesystem, flip: func() error {
			flipped = true
			return filesystem.(sandbox.ManagedSkillPublisher).PublishManagedSkill(ctx, sandbox.PathWorkspace, "pinned", before.ContentDigest, skillTreePublication(oldTree))
		}}
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := pinnedCatalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !flipped || pinned.ContentDigest == before.ContentDigest || string(pinned.Files[1].Content) != "new" {
		t.Fatalf("snapshot did not pin inspected revision: %+v", pinned)
	}

	root, err := home.UserSkillCatalog(userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.homes.UseSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, path.Join(sandbox.PathWorkspace, "ordinary"), 0o755); err != nil {
			return err
		}
		return filesystem.Write(ctx, path.Join(sandbox.PathWorkspace, "ordinary", MainFile), strings.NewReader(string(managedSkillMarkdown("ordinary", "ordinary", "body"))), sandbox.WriteOptions{})
	}); err != nil {
		t.Fatal(err)
	}
	ordinaryID, err := encodeFilesystemSkillID("user", userID, "", "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadManagedSnapshot(ctx, ordinaryID); !errors.Is(err, ErrSkillNotMutable) {
		t.Fatalf("ordinary snapshot = %v", err)
	}
}

func TestHomeCatalogLoadManagedSnapshotFailsClosedForRemovedOrExpandedRevision(t *testing.T) {
	manager, _, ctx, migration, userID, _, _ := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "adversarial", Description: "before", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("adversarial", "before", "body")}}})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		return &removeRevisionAfterInspectFilesystem{Filesystem: filesystem}
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := removed.LoadManagedSnapshot(ctx, created.Skill.ID); err == nil {
		t.Fatal("removed selected revision was accepted")
	}

	// Recreate the direct selection that the removal wrapper deliberately
	// destroyed, then make a later manifest claim hundreds of huge files. The
	// capture must reject from manifest sizes before it opens any file body.
	before, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "expanded", Description: "before", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("expanded", "before", "body")}}})
	if err != nil {
		t.Fatal(err)
	}
	expanded := &expandRevisionManifestFilesystem{}
	expandingCatalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		expanded.Filesystem = filesystem
		return expanded
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expandingCatalog.LoadManagedSnapshot(ctx, before.Skill.ID); err == nil {
		t.Fatal("over-budget manifest was accepted")
	}
	if expanded.reads != 0 {
		t.Fatalf("over-budget manifest opened %d files", expanded.reads)
	}
}

func TestHomeSkillManagerKeepsLargeSkillMarkdownCatalogReadable(t *testing.T) {
	manager, catalog, ctx, _, userID, _, _ := newHomeSkillManagerFixture(t)
	body := strings.Repeat("x", (1<<20)+123)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "large-main", Description: "large", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("large-main", "large", body)}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil || len(loaded.Files[0].Content) <= maxCatalogSkillBytes {
		t.Fatalf("large load = %d bytes, %v", len(loaded.Files[0].Content), err)
	}
	updated, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "note", Content: []byte("ok")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, updated.Skill.ID, updated.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
}

func TestHomeSkillManagerHandlesEmptyCompanionAcrossLifecycle(t *testing.T) {
	manager, catalog, ctx, _, userID, _, _ := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "empty-companion", Description: "empty", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("empty-companion", "empty", "body")}, {Path: "empty", Content: nil}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil || len(loaded.Files[1].Content) != 0 {
		t.Fatalf("empty companion load = %+v, %v", loaded, err)
	}
	updated, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.Skill.ID, ExpectedDigest: created.Skill.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "note", Content: []byte("updated")}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = catalog.LoadManagedSnapshot(ctx, updated.Skill.ID)
	if err != nil || len(loaded.Files[1].Content) != 0 {
		t.Fatalf("empty companion update = %+v, %v", loaded, err)
	}
	afterFileDelete, err := manager.DeleteFile(ctx, updated.Skill.ID, updated.Skill.ContentDigest, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, afterFileDelete.Skill.ID, afterFileDelete.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
}

func TestHomeCatalogDefaultsMigratedFrontmatterNameThenManagerCanonicalizesIt(t *testing.T) {
	manager, catalog, ctx, _, userID, _, now := newHomeSkillManagerFixture(t)
	root, err := home.UserSkillCatalog(userID)
	if err != nil {
		t.Fatal(err)
	}
	const name = "migrated-name"
	legacyMain := []byte("---\ndescription: migrated description\n---\nbody\n")
	digest, err := manager.publisher.Publish(ctx, HomeSkillPublishRequest{
		Root: root, Name: name,
		Metadata: HomeSkillMetadata{Status: SkillStatusActive, Metadata: map[string]any{"created_by": ManualSkillCreatedBy}, CreatedAt: *now, UpdatedAt: *now, LegacyLifecycleVersion: 1},
		Files:    []HomeSkillFile{{Path: MainFile, Content: legacyMain, Mode: 0o644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := encodeFilesystemSkillID("user", userID, "", name)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, id)
	if err != nil || loaded.Skill.Name != name || loaded.Skill.Description != "migrated description" {
		t.Fatalf("migrated-style load = %+v, %v", loaded, err)
	}
	updated, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: id, ExpectedDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = catalog.LoadManagedSnapshot(ctx, id)
	if err != nil || !strings.Contains(string(loaded.Files[0].Content), "name: migrated-name") || loaded.ContentDigest != updated.Skill.ContentDigest {
		t.Fatalf("migrated-style canonical update = %+v, %v", loaded, err)
	}
}

func TestHomeSkillManagerRejectsInvalidLocalInputsBeforeHomeAccess(t *testing.T) {
	manager, _, ctx, migration, userID, _, _ := newHomeSkillManagerFixture(t)
	calls := 0
	manager.publisher.homes = homeSkillFilesystemAccessFunc(func(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		calls++
		return migration.homes.UseSkillFilesystem(ctx, root, use)
	})
	valid := func() HomeSkillCreateRequest {
		return HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "validation", Description: "valid", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("validation", "valid", "body")}}}
	}
	badMode := fs.ModeSetuid | 0o644
	for name, mutate := range map[string]func(*HomeSkillCreateRequest){
		"invalid path": func(request *HomeSkillCreateRequest) {
			request.Files = append(request.Files, HomeSkillFileInput{Path: "../escape", Content: []byte("x")})
		},
		"special mode": func(request *HomeSkillCreateRequest) { request.Files[0].Mode = &badMode },
		"file limit":   func(request *HomeSkillCreateRequest) { request.Files[0].Content = make([]byte, maxManagedFileBytes+1) },
		"metadata limit": func(request *HomeSkillCreateRequest) {
			request.Metadata = map[string]any{"source": strings.Repeat("x", maxCatalogMetadataBytes)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid()
			mutate(&request)
			if _, err := manager.Create(ctx, request); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid input opened Home %d times", calls)
	}
}

func TestHomeSkillManagerCreateConflictsSnapshotsCallerBytesAndPreservesUnknownOutcome(t *testing.T) {
	manager, catalog, ctx, migration, userID, _, _ := newHomeSkillManagerFixture(t)
	content := managedSkillMarkdown("conflict", "description", "body")
	request := HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "conflict", Description: "description", Files: []HomeSkillFileInput{{Path: MainFile, Content: content}}}
	manager.publisher.homes = homeSkillFilesystemAccessFunc(func(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		content[len(content)-1] = 'X'
		return migration.homes.UseSkillFilesystem(ctx, root, use)
	})
	created, err := manager.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil || !strings.HasSuffix(string(loaded.Files[0].Content), "body") {
		t.Fatalf("caller mutation reached published revision: %q, %v", loaded.Files[0].Content, err)
	}
	if _, err := manager.Create(ctx, request); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("duplicate create = %v", err)
	}
	root, err := home.UserSkillCatalog(userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.homes.UseSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		return filesystem.Mkdir(ctx, path.Join(sandbox.PathWorkspace, "ordinary-create"), 0o755)
	}); err != nil {
		t.Fatal(err)
	}
	ordinary := request
	ordinary.Name = "ordinary-create"
	ordinary.Files = []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("ordinary-create", "description", "body")}}
	if _, err := manager.Create(ctx, ordinary); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("ordinary create = %v", err)
	}

	unknown := &genericErrorPublishFilesystem{err: errors.New("publisher disconnected")}
	manager.publisher.homes = homeSkillFilesystemAccessFunc(func(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return migration.homes.UseSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
			unknown.Filesystem = filesystem
			return use(unknown)
		})
	})
	unknownRequest := ordinary
	unknownRequest.Name = "unknown-create"
	unknownRequest.Files = []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("unknown-create", "description", "body")}}
	if _, err := manager.Create(ctx, unknownRequest); !errors.Is(err, sandbox.ErrOutcomeUnknown) || unknown.publishes != 1 {
		t.Fatalf("publisher outcome unknown = %v, publishes=%d", err, unknown.publishes)
	}
}

func TestHomeSkillManagerRejectsDuplicateYAMLAndPreservesCRLFBody(t *testing.T) {
	duplicate := []byte("---\nname: duplicate\nname: duplicate\ndescription: description\n---\nbody")
	if _, err := rewriteSkillFrontmatter(duplicate, "duplicate", "description"); err == nil {
		t.Fatal("duplicate YAML authority key was accepted")
	}
	if _, err := rewriteSkillFrontmatter([]byte("---\nname: missing\n---\nbody"), "missing", "description"); err == nil {
		t.Fatal("missing frontmatter description was accepted")
	}
	got, err := rewriteSkillFrontmatter([]byte("---\r\nname: crlf\r\ndescription: source\r\n---\r\nbody\r\n"), "crlf", "final")
	if err != nil || !strings.HasSuffix(string(got), "---\nbody\r\n") {
		t.Fatalf("CRLF body boundary = %q, %v", got, err)
	}
}

func TestHomeCatalogLoadManagedSnapshotUsesOneCapturedSkillMarkdownForSemantics(t *testing.T) {
	manager, _, ctx, migration, userID, _, _ := newHomeSkillManagerFixture(t)
	created, err := manager.Create(ctx, HomeSkillCreateRequest{Scope: "user", UserID: userID, Name: "aba-main", Description: "before", Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("aba-main", "before", "body")}}})
	if err != nil {
		t.Fatal(err)
	}
	flipping := &flipMainAfterReadFilesystem{}
	catalog, err := NewHomeCatalog(callbackWrappingHomeCatalog{inner: migration.homes, wrap: func(filesystem sandbox.Filesystem) sandbox.Filesystem {
		flipping.Filesystem = filesystem
		return flipping
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadManagedSnapshot(ctx, created.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flipping.mainReads != 1 || loaded.Skill.Description != "before" || !strings.Contains(string(loaded.Files[0].Content), "description: before") {
		t.Fatalf("semantic ABA leaked across snapshot: reads=%d snapshot=%+v", flipping.mainReads, loaded)
	}
}

type flipManagedTargetFilesystem struct {
	sandbox.Filesystem
	flipped bool
	flip    func() error
}

type removeRevisionAfterInspectFilesystem struct {
	sandbox.Filesystem
	removed bool
}

func (f *removeRevisionAfterInspectFilesystem) InspectManagedSkillTarget(ctx context.Context, entry string) (sandbox.ManagedSkillTarget, error) {
	target, err := f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, entry)
	if err == nil && target.Managed && !f.removed {
		f.removed = true
		if removeErr := f.Remove(ctx, path.Join(sandbox.PathWorkspace, ".stella-revisions", path.Base(entry), target.Digest), true); removeErr != nil {
			return sandbox.ManagedSkillTarget{}, removeErr
		}
	}
	return target, err
}

type expandRevisionManifestFilesystem struct {
	sandbox.Filesystem
	expanded bool
	reads    int
}

func (f *expandRevisionManifestFilesystem) InspectManagedSkillTarget(ctx context.Context, entry string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, entry)
}

func (f *expandRevisionManifestFilesystem) List(ctx context.Context, directory string) ([]sandbox.DirEntry, error) {
	entries, err := f.Filesystem.List(ctx, directory)
	if err != nil || f.expanded || !strings.Contains(directory, "/.stella-revisions/") {
		return entries, err
	}
	f.expanded = true
	for i := range 509 { // Bounded metadata only: 509 × 8 MiB is never allocated or read.
		entries = append(entries, sandbox.DirEntry{Name: fmt.Sprintf("large-%03d", i), Size: maxManagedFileBytes, Mode: 0o644})
	}
	return entries, nil
}

func (f *expandRevisionManifestFilesystem) Read(ctx context.Context, filename string, options sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	f.reads++
	return f.Filesystem.Read(ctx, filename, options)
}

type flipMainAfterReadFilesystem struct {
	sandbox.Filesystem
	mainReads int
}

func (f *flipMainAfterReadFilesystem) InspectManagedSkillTarget(ctx context.Context, entry string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, entry)
}

func (f *flipMainAfterReadFilesystem) Read(ctx context.Context, filename string, options sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	reader, info, err := f.Filesystem.Read(ctx, filename, options)
	if err != nil || path.Base(filename) != MainFile {
		return reader, info, err
	}
	f.mainReads++
	captured, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		if readErr != nil {
			return nil, sandbox.FileInfo{}, readErr
		}
		return nil, sandbox.FileInfo{}, closeErr
	}
	if err := f.Write(ctx, filename, strings.NewReader(string(managedSkillMarkdown("aba-main", "after", "changed"))), sandbox.WriteOptions{}); err != nil {
		return nil, sandbox.FileInfo{}, err
	}
	return io.NopCloser(bytes.NewReader(captured)), info, nil
}

func (f *flipManagedTargetFilesystem) InspectManagedSkillTarget(ctx context.Context, entry string) (sandbox.ManagedSkillTarget, error) {
	target, err := f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, entry)
	if err == nil && !f.flipped {
		f.flipped = true
		if flipErr := f.flip(); flipErr != nil {
			return sandbox.ManagedSkillTarget{}, flipErr
		}
	}
	return target, err
}

func containsAll(value string, want ...string) bool {
	for _, part := range want {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
