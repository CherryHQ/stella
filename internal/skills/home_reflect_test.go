package skills

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func newHomeReflectFixture(t *testing.T) (*HomeReflectStore, *HomeSkillManager, *HomeSkillUsageStore, *pgxpool.Pool, context.Context, *skillMigrationService, string, string, *time.Time) {
	t.Helper()
	t.Setenv("STELLA_HOME", t.TempDir())
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
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	manager, err := NewHomeSkillManager(catalog, publisher, func() time.Time { return now })
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
	store, err := NewHomeReflectStore(homeStore, usage)
	if err != nil {
		t.Fatal(err)
	}
	return store, manager, usage, db, ctx, migration, userID, agentID, &now
}

func TestHomeAuthorityStoreDelegatesOneAuthorityAndRejectsSplit(t *testing.T) {
	reflectStore, manager, _, _, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
	store, err := NewHomeAuthorityStore(reflectStore.home, reflectStore)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManagedSkill(ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "composite", Description: "before"}, map[string]string{MainFile: catalogBody("composite")})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, created.Skill.ID); err != nil || got.ContentDigest != created.Skill.ContentDigest {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	after, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: userID, AgentID: agentID, ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{"note": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteManagedSkillFile(ctx, ManagedSkillFileDelete{ManagedSkillDelete: ManagedSkillDelete{ID: after.Skill.ID, Scope: after.Skill.Scope, UserID: userID, AgentID: agentID, ExpectedDigest: after.Skill.ContentDigest}, Path: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{UserID: userID, AgentID: agentID, Name: "reflect-composite", Description: "reflect", MainFileContent: string(managedSkillMarkdown("reflect-composite", "reflect", "body"))}); err != nil {
		t.Fatal(err)
	}
	other, err := NewHomeStore(reflectStore.home.catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHomeAuthorityStore(other, reflectStore); err == nil {
		t.Fatal("composite accepted split HomeStore pointer")
	}
	if _, err := NewHomeAuthorityStore(nil, reflectStore); err == nil {
		t.Fatal("composite accepted nil HomeStore")
	}
}

func TestHomeAuthorityStoreHasNoLegacySkillStateDependency(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "home_authority_store.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PGStore", "skill_changelog", "sqlc", "GetSkillForUpdate"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("Home authority composite references legacy state %q", forbidden)
		}
	}
}

func TestHomeReflectCreatePatchRetriesAndRetainsRevisionProvenance(t *testing.T) {
	store, manager, usage, db, ctx, _, userID, agentID, now := newHomeReflectFixture(t)
	create := ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "home-reflect", Description: "first revision",
		MainFileContent:   "---\ndescription: ignored source\n---\nfirst body\n",
		Metadata:          json.RawMessage(`{"source":"reflect"}`),
		ChangelogMetadata: json.RawMessage(`{"reflect_provenance":{"operation_ref":"create-1"},"run":"one"}`),
	}
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || !validHomeSkillDigest(created.ContentDigest) || CreatedBy(created) != ReflectSkillCreatedBy || created.Status != SkillStatusActive {
		t.Fatalf("created Home Reflect Skill = %#v", created)
	}
	var skillRows, changelogRows, fileRows int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM skill").Scan(&skillRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM skill_changelog").Scan(&changelogRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM skill_file").Scan(&fileRows); err != nil {
		t.Fatal(err)
	}
	if skillRows != 0 || changelogRows != 0 || fileRows != 0 {
		t.Fatalf("Home Reflect wrote legacy current state: skill=%d changelog=%d files=%d", skillRows, changelogRows, fileRows)
	}
	firstUsage, err := usage.Get(ctx, homeReflectUsageIdentity(created))
	if err != nil {
		t.Fatal(err)
	}
	retriedCreate, err := store.CreateReflectOwnedUserAgentSkill(ctx, create)
	if err != nil || retriedCreate.ID != created.ID || retriedCreate.UpdatedAt != created.UpdatedAt {
		t.Fatalf("exact create retry = %#v, %v", retriedCreate, err)
	}
	usageAfterCreateRetry, err := usage.Get(ctx, homeReflectUsageIdentity(created))
	if err != nil || usageAfterCreateRetry.UseCount != firstUsage.UseCount || !usageAfterCreateRetry.LastUsedAt.Equal(firstUsage.LastUsedAt) {
		t.Fatalf("create retry usage = %#v, %v", usageAfterCreateRetry, err)
	}

	// Reflect's narrow request type does not expose companions. Seed one as the
	// pre-existing complete tree then move the logical telemetry to that exact
	// revision; the patch must retain its opaque bytes and executable mode.
	executable := fs.FileMode(0o755)
	*now = now.Add(time.Second)
	withCompanion, err := manager.Update(ctx, HomeSkillUpdateRequest{
		ID: created.ID, ExpectedDigest: created.ContentDigest,
		FileUpserts: []HomeSkillFileInput{{Path: "tools/run", Content: []byte{0, 1, 2, 255}, Mode: &executable}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.PatchReflectDigest(ctx, homeReflectUsageIdentity(created), withCompanion.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
	beforePatchUsage, err := usage.Get(ctx, homeReflectUsageIdentity(withCompanion.Skill))
	if err != nil {
		t.Fatal(err)
	}
	patchBody := "---\nname: wrong\ndescription: ignored source\n---\nsecond body\n"
	patchDescription := "second revision"
	patch := ReflectSkillPatch{
		ID: withCompanion.Skill.ID, UserID: userID, AgentID: agentID, ExpectedDigest: withCompanion.Skill.ContentDigest,
		Description: &patchDescription, MainFileContent: &patchBody,
		Metadata:          json.RawMessage(`{"source":"reflect-patched"}`),
		ChangelogMetadata: json.RawMessage(`{"reflect_provenance":{"operation_ref":"patch-2"},"run":"two"}`),
	}
	*now = now.Add(time.Second)
	patched, err := store.PatchReflectOwnedUserAgentSkill(ctx, patch)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patched.ContentDigest == withCompanion.Skill.ContentDigest || patched.Description != patchDescription {
		t.Fatalf("patched = %#v", patched)
	}
	loaded, err := store.home.catalog.LoadManagedSnapshot(ctx, patched.ID)
	if err != nil || len(loaded.Files) != 2 || string(loaded.Files[1].Content) != string([]byte{0, 1, 2, 255}) || loaded.Files[1].Mode != executable {
		t.Fatalf("patched tree = %#v, %v", loaded.Files, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(patched.Metadata, &metadata); err != nil || metadata["created_by"] != ReflectSkillCreatedBy {
		t.Fatalf("patched provenance metadata = %s, %v", patched.Metadata, err)
	}
	audit, ok := metadata[reflectLastChangeMetadataKey].(map[string]any)
	if !ok || audit["run"] != "two" {
		t.Fatalf("reserved provenance metadata = %#v", metadata)
	}
	if _, leaked := metadata["run"]; leaked {
		t.Fatalf("changelog metadata leaked into entity metadata: %#v", metadata)
	}
	patchedUsage, err := usage.Get(ctx, homeReflectUsageIdentity(patched))
	if err != nil || patchedUsage.UseCount != beforePatchUsage.UseCount {
		t.Fatalf("patched usage = %#v, %v", patchedUsage, err)
	}
	retriedPatch, err := store.PatchReflectOwnedUserAgentSkill(ctx, patch)
	if err != nil || retriedPatch.ContentDigest != patched.ContentDigest || !retriedPatch.UpdatedAt.Equal(patched.UpdatedAt) {
		t.Fatalf("exact patch retry = %#v, %v", retriedPatch, err)
	}
	usageAfterPatchRetry, err := usage.Get(ctx, homeReflectUsageIdentity(patched))
	if err != nil || usageAfterPatchRetry.UseCount != patchedUsage.UseCount || !usageAfterPatchRetry.LastUsedAt.Equal(patchedUsage.LastUsedAt) {
		t.Fatalf("patch retry usage = %#v, %v", usageAfterPatchRetry, err)
	}

	staleBody := "---\ndescription: stale\n---\nstale\n"
	stale := patch
	stale.MainFileContent = &staleBody
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, stale); !errors.Is(err, ErrHomeSkillConflict) || sandbox.IsOutcomeUnknown(err) {
		t.Fatalf("stale patch = %v", err)
	}
	still, err := store.home.catalog.LoadManagedSnapshot(ctx, patched.ID)
	if err != nil || still.ContentDigest != patched.ContentDigest {
		t.Fatalf("stale patch changed Home revision = %#v, %v", still, err)
	}
}

func TestHomeReflectStalePatchRetryDerivesFromRetainedExpectedRevision(t *testing.T) {
	t.Run("coincident main cannot hide companion or metadata change", func(t *testing.T) {
		store, manager, usage, _, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
		created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
			UserID: userID, AgentID: agentID, Name: "retained-companion", Description: "before",
			MainFileContent: "---\ndescription: before\n---\nA\n", Metadata: json.RawMessage(`{"source":"A"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		mode := fs.FileMode(0o755)
		observed, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.ID, ExpectedDigest: created.ContentDigest, FileUpserts: []HomeSkillFileInput{{Path: "tools/run", Content: []byte("old"), Mode: &mode}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := usage.PatchReflectDigest(ctx, homeReflectUsageIdentity(created), observed.Skill.ContentDigest); err != nil {
			t.Fatal(err)
		}
		body := "---\ndescription: ignored\n---\nB\n"
		patch := ReflectSkillPatch{ID: observed.Skill.ID, UserID: userID, AgentID: agentID, ExpectedDigest: observed.Skill.ContentDigest, MainFileContent: &body, ChangelogMetadata: json.RawMessage(`{"reflect_provenance":{"operation_ref":"B"}}`)}
		metadata, err := mergeHomeReflectMetadataFromJSON(observed.Skill.Metadata, json.RawMessage(`{"source":"concurrent"}`), patch.ChangelogMetadata)
		if err != nil {
			t.Fatal(err)
		}
		current, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: observed.Skill.ID, ExpectedDigest: observed.Skill.ContentDigest, Metadata: &metadata, FileUpserts: []HomeSkillFileInput{{Path: MainFile, Content: []byte(body)}, {Path: "tools/run", Content: []byte("concurrent"), Mode: &mode}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.home.catalog.LoadManagedRevision(ctx, observed.Skill.ID, observed.Skill.ContentDigest); err != nil {
			t.Fatalf("retained expected revision is not readable: %v", err)
		}
		if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, patch); !errors.Is(err, ErrHomeSkillConflict) {
			t.Fatalf("false-positive stale retry = %v", err)
		}
		if _, err := usage.Get(ctx, homeReflectUsageIdentity(observed.Skill)); err != nil {
			t.Fatalf("false-positive retry changed logical usage: %v", err)
		}
		still, err := store.home.catalog.LoadManagedSnapshot(ctx, current.Skill.ID)
		if err != nil || still.ContentDigest != current.Skill.ContentDigest {
			t.Fatalf("false-positive retry changed current revision = %#v, %v", still, err)
		}
	})

	t.Run("omitted body cannot inherit concurrent body", func(t *testing.T) {
		store, manager, usage, _, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
		created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{UserID: userID, AgentID: agentID, Name: "retained-body", Description: "before", MainFileContent: "---\ndescription: before\n---\nA\n"})
		if err != nil {
			t.Fatal(err)
		}
		patch := ReflectSkillPatch{ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest, ChangelogMetadata: json.RawMessage(`{"reflect_provenance":{"operation_ref":"B"}}`)}
		metadata, err := mergeHomeReflectMetadataFromJSON(created.Metadata, nil, patch.ChangelogMetadata)
		if err != nil {
			t.Fatal(err)
		}
		current, err := manager.Update(ctx, HomeSkillUpdateRequest{ID: created.ID, ExpectedDigest: created.ContentDigest, Metadata: &metadata, FileUpserts: []HomeSkillFileInput{{Path: MainFile, Content: []byte("---\ndescription: before\n---\nconcurrent\n")}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, patch); !errors.Is(err, ErrHomeSkillConflict) {
			t.Fatalf("omitted-body false-positive retry = %v", err)
		}
		if _, err := usage.Get(ctx, homeReflectUsageIdentity(created)); err != nil {
			t.Fatalf("omitted-body retry changed logical usage: %v", err)
		}
		still, err := store.home.catalog.LoadManagedSnapshot(ctx, current.Skill.ID)
		if err != nil || still.ContentDigest != current.Skill.ContentDigest {
			t.Fatalf("omitted-body retry changed current revision = %#v, %v", still, err)
		}
	})
}

func TestHomeReflectReservesLastChangeMetadata(t *testing.T) {
	store, _, _, _, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "audit-isolation", Description: "audit isolation",
		MainFileContent: "---\ndescription: audit isolation\n---\nbody\n", Metadata: json.RawMessage(`{"source":"entity"}`), ChangelogMetadata: json.RawMessage(`{"source":"audit","reflect_provenance":{"operation_ref":"one"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(created.Metadata, &metadata); err != nil || metadata["source"] != "entity" {
		t.Fatalf("entity metadata = %s, %v", created.Metadata, err)
	}
	audit, ok := metadata[reflectLastChangeMetadataKey].(map[string]any)
	if !ok || audit["source"] != "audit" {
		t.Fatalf("audit metadata = %#v", metadata)
	}
	_, err = store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reserved-provenance", Description: "reserved",
		MainFileContent: "---\ndescription: reserved\n---\nbody\n", Metadata: json.RawMessage(`{"reflect_last_change":{}}`),
	})
	if err == nil {
		t.Fatal("entity metadata overwrote reserved audit key")
	}
	body := "---\ndescription: audit isolation\n---\npatched\n"
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest, MainFileContent: &body, Metadata: json.RawMessage(`{"reflect_last_change":{}}`)}); err == nil {
		t.Fatal("patch entity metadata overwrote reserved audit key")
	}
}

func TestHomeReflectDeleteUsesActivityCASAndConcurrentTouchHasOneWinner(t *testing.T) {
	store, _, usage, db, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "curated-home-reflect", Description: "curated",
		MainFileContent: "---\ndescription: curated\n---\nbody\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := homeReflectUsageIdentity(created)
	current, err := usage.Get(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest, ExpectedUsageLastUsedAt: current.LastUsedAt, ExpectedPairLatestActivityAt: current.LastUsedAt.Add(time.Hour)}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("delete without eligible activity = %v", err)
	}
	if _, err := store.home.catalog.LoadManagedSnapshot(ctx, created.ID); err != nil {
		t.Fatalf("no-activity delete changed Home: %v", err)
	}
	activityAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id)
		VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "home-reflect-curator", activityAt, agentID, userID); err != nil {
		t.Fatal(err)
	}

	// A fresh exact timestamp plus pair activity allows either the touch or the
	// delete to win; neither loser may report a clean delete.
	current, err = usage.Get(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	touchResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	go func() {
		<-start
		touchResult <- store.TouchReflectSkillRuntimeUse(ctx, created.ID, userID, agentID, created.ContentDigest)
	}()
	go func() {
		<-start
		_, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest, ExpectedUsageLastUsedAt: current.LastUsedAt, ExpectedPairLatestActivityAt: activityAt})
		deleteResult <- err
	}()
	close(start)
	touchErr, deleteErr := <-touchResult, <-deleteResult
	switch {
	case touchErr == nil && deleteErr == nil:
		t.Fatal("touch and delete both succeeded")
	case deleteErr == nil:
		if !errors.Is(touchErr, ErrSkillUsageChanged) {
			t.Fatalf("delete winner touch = %v, want usage conflict", touchErr)
		}
	case touchErr == nil:
		if !errors.Is(deleteErr, ErrSkillUsageChanged) {
			t.Fatalf("touch winner delete = %v, want usage conflict", deleteErr)
		}
	default:
		t.Fatalf("race had no legitimate winner: touch=%v delete=%v", touchErr, deleteErr)
	}
}

func TestHomeReflectRejectsOrdinaryAndOwnerMismatchesBeforeUsage(t *testing.T) {
	store, manager, _, _, ctx, _, userID, agentID, _ := newHomeReflectFixture(t)
	manual, err := manager.Create(ctx, HomeSkillCreateRequest{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "manual-home-reflect", Description: "manual",
		Files: []HomeSkillFileInput{{Path: MainFile, Content: managedSkillMarkdown("manual-home-reflect", "manual", "body")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: changed\n---\nbody\n"
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{ID: manual.Skill.ID, UserID: userID, AgentID: agentID, ExpectedDigest: manual.Skill.ContentDigest, MainFileContent: &body}); !errors.Is(err, ErrSkillNotReflectOwned) {
		t.Fatalf("manual patch = %v, want owner rejection", err)
	}
	if _, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: manual.Skill.Name, Description: "manual",
		MainFileContent: "---\ndescription: manual\n---\nbody\n",
	}); !errors.Is(err, ErrHomeSkillConflict) {
		t.Fatalf("ordinary create conflict = %v", err)
	}
	wrongUser := uuid.NewString()
	if err := store.TouchReflectSkillRuntimeUse(ctx, manual.Skill.ID, wrongUser, agentID, manual.Skill.ContentDigest); err == nil {
		t.Fatal("runtime touch accepted mismatched canonical owner")
	}
}

func TestHomeReflectMarksPostPublicationAndPostTelemetryFailuresUnknown(t *testing.T) {
	t.Run("usage after publication", func(t *testing.T) {
		store, manager, _, db, ctx, migration, userID, agentID, _ := newHomeReflectFixture(t)
		manager.publisher.homes = homeSkillFilesystemAccessFunc(func(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
			err := migration.homes.UseSkillFilesystem(ctx, root, use)
			db.Close() // The publish callback has returned: only telemetry remains.
			return err
		})
		_, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
			UserID: userID, AgentID: agentID, Name: "unknown-usage", Description: "unknown",
			MainFileContent: "---\ndescription: unknown\n---\nbody\n",
		})
		if !sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("telemetry after publication = %v, want outcome unknown", err)
		}
	})

	t.Run("unpublish after telemetry", func(t *testing.T) {
		store, manager, usage, db, ctx, migration, userID, agentID, _ := newHomeReflectFixture(t)
		created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
			UserID: userID, AgentID: agentID, Name: "unknown-unpublish", Description: "unknown",
			MainFileContent: "---\ndescription: unknown\n---\nbody\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		usageBefore, err := usage.Get(ctx, homeReflectUsageIdentity(created))
		if err != nil {
			t.Fatal(err)
		}
		activityAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
		if _, err := db.Exec(ctx, `
			INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id)
			VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "unknown-unpublish", activityAt, agentID, userID); err != nil {
			t.Fatal(err)
		}
		manager.publisher.homes = homeSkillFilesystemAccessFunc(func(ctx context.Context, root *home.SkillRoot, use func(sandbox.Filesystem) error) error {
			return migration.homes.UseSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
				return use(&homeReflectFailUnpublishFilesystem{Filesystem: filesystem, err: errors.New("unpublisher disconnected")})
			})
		})
		_, err = store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{
			ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest, ExpectedUsageLastUsedAt: usageBefore.LastUsedAt, ExpectedPairLatestActivityAt: activityAt,
		})
		if !sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("unpublish after telemetry = %v, want outcome unknown", err)
		}
		if _, err := usage.Get(ctx, homeReflectUsageIdentity(created)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("usage rollback after unpublish failure = %v, want deleted", err)
		}
	})
}

type homeReflectFailUnpublishFilesystem struct {
	sandbox.Filesystem
	err error
}

func (f *homeReflectFailUnpublishFilesystem) UnpublishManagedSkill(context.Context, string, string, string) error {
	return f.err
}

func (f *homeReflectFailUnpublishFilesystem) InspectManagedSkillTarget(ctx context.Context, entry string) (sandbox.ManagedSkillTarget, error) {
	return f.Filesystem.(sandbox.ManagedSkillTargetInspector).InspectManagedSkillTarget(ctx, entry)
}

func TestHomeReflectBoundaryDoesNotReferenceLegacySkillCurrentState(t *testing.T) {
	source, err := fs.ReadFile(os.DirFS("."), "home_reflect.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"PGStore", "skill_changelog", "CreateSkill(", "GetSkillForUpdate("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Home Reflect boundary references legacy current state %q", forbidden)
		}
	}
}
