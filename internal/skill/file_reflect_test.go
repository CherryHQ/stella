package skill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
)

func TestReflectFilesystemConflict(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000321"
	const agentID = "file-reflect-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-reflect@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'File reflect','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := NewFileStore(db, manager)
	ctx := t.Context()

	created, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-file", Description: "from Reflect", MainFileContent: "# first\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 0 || !validSkillDigest(created.ContentDigest) || !isFileSkillID(created.ID) {
		t.Fatalf("created file Skill = %#v", created)
	}
	var identityCount, changelogCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill WHERE id=$1`, created.ID).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 0 {
		t.Fatalf("file Skill created a database identity row: %d", identityCount)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, created.ID).Scan(&changelogCount); err != nil {
		t.Fatal(err)
	}
	if changelogCount != 1 {
		t.Fatalf("create evidence count = %d, want 1", changelogCount)
	}
	var usageBefore int
	var lastUsedBefore time.Time
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at FROM skill_usage WHERE skill_id=$1`, created.ID).Scan(&usageBefore, &lastUsedBefore); err != nil {
		t.Fatal(err)
	}
	retried, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-file", Description: "from Reflect", MainFileContent: "# first\n",
	})
	if err != nil {
		t.Fatalf("idempotent Reflect create retry: %v", err)
	}
	if retried.ID != created.ID || retried.ContentDigest != created.ContentDigest {
		t.Fatalf("idempotent retry = %#v, want original %#v", retried, created)
	}
	var retryHistory, usageAfter int
	var lastUsedAfter time.Time
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, created.ID).Scan(&retryHistory); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at FROM skill_usage WHERE skill_id=$1`, created.ID).Scan(&usageAfter, &lastUsedAfter); err != nil {
		t.Fatal(err)
	}
	if retryHistory != changelogCount || usageAfter != usageBefore || !lastUsedAfter.Equal(lastUsedBefore) {
		t.Fatalf("idempotent retry changed evidence/usage: history=%d use_count=%d last_used_at=%s", retryHistory, usageAfter, lastUsedAfter)
	}

	root := filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills", created.Name)
	mainPath := filepath.Join(root, MainFile)
	main, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte(strings.Replace(string(main), "# first", "# changed by bash", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchReflectOwnedUserAgentSkill(ctx, ReflectSkillPatch{
		ID: created.ID, UserID: userID, AgentID: agentID, ExpectedDigest: created.ContentDigest,
		MainFileContent: stringPtr("# second\n"),
	}); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale Reflect patch after bash edit = %v, want digest conflict", err)
	}
	active, err := store.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("manually changed file remained Reflect-eligible: %#v", active)
	}

	forged, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	forgedText := strings.Replace(string(forged), "metadata:", "metadata:\n  created_by: reflect", 1)
	if err := os.WriteFile(mainPath, []byte(forgedText), 0o644); err != nil {
		t.Fatal(err)
	}
	active, err = store.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("forged created_by restored Reflect eligibility: %#v", active)
	}

	deletable, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-delete", Description: "delete me", MainFileContent: "# delete\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var usageAt time.Time
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id=$1`, deletable.ID).Scan(&usageAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE skill_usage SET content_digest='0000000000000000000000000000000000000000000000000000000000000000' WHERE skill_id=$1`, deletable.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: deletable.ID, UserID: userID, AgentID: agentID, ExpectedDigest: deletable.ContentDigest, ExpectedUsageLastUsedAt: usageAt}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("stale usage delete = %v, want usage changed", err)
	}
	if _, err := os.Stat(filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills", deletable.Name, MainFile)); err != nil {
		t.Fatalf("stale usage delete removed source file: %v", err)
	}

	missing, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-missing", Description: "missing", MainFileContent: "# missing\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var missingUsageAt time.Time
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id=$1`, missing.ID).Scan(&missingUsageAt); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills", missing.Name)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: missing.ID, UserID: userID, AgentID: agentID, ExpectedDigest: missing.ContentDigest, ExpectedUsageLastUsedAt: missingUsageAt}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("missing source delete = %v, want usage changed", err)
	}

	manualEvidence, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-manual-evidence", Description: "manual evidence", MainFileContent: "# manual evidence\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var manualEvidenceUsageAt time.Time
	if err := db.QueryRow(ctx, `SELECT last_used_at FROM skill_usage WHERE skill_id=$1`, manualEvidence.ID).Scan(&manualEvidenceUsageAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO skill_changelog (skill_id,user_id,agent_id,scope,action,version_after,content_digest,writer,metadata) VALUES ($1,$2,$3,'user_agent','patch',0,$4,'manual','{}')`, manualEvidence.ID, userID, agentID, manualEvidence.ContentDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: manualEvidence.ID, UserID: userID, AgentID: agentID, ExpectedDigest: manualEvidence.ContentDigest, ExpectedUsageLastUsedAt: manualEvidenceUsageAt}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("manual evidence delete = %v, want usage changed", err)
	}

	touchDelete, err := store.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{
		UserID: userID, AgentID: agentID, Name: "reflect-touch-delete", Description: "touch then delete", MainFileContent: "# touch then delete\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	touchPath := filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills", touchDelete.Name, MainFile)
	beforeTouchDelete, err := os.ReadFile(touchPath)
	if err != nil {
		t.Fatal(err)
	}
	var beforeTouchCount int
	var beforeTouchLastUsed time.Time
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at FROM skill_usage WHERE skill_id=$1`, touchDelete.ID).Scan(&beforeTouchCount, &beforeTouchLastUsed); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchReflectSkillRuntimeUseDigest(ctx, touchDelete.ID, userID, agentID, touchDelete.ContentDigest); err != nil {
		t.Fatalf("touch Reflect skill: %v", err)
	}
	var touchedCount int
	var touchedLastUsed time.Time
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at FROM skill_usage WHERE skill_id=$1`, touchDelete.ID).Scan(&touchedCount, &touchedLastUsed); err != nil {
		t.Fatal(err)
	}
	if touchedCount != beforeTouchCount+1 || !touchedLastUsed.After(beforeTouchLastUsed) {
		t.Fatalf("touched usage = count %d at %s, want count %d after %s", touchedCount, touchedLastUsed, beforeTouchCount+1, beforeTouchLastUsed)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: touchDelete.ID, UserID: userID, AgentID: agentID, ExpectedDigest: touchDelete.ContentDigest, ExpectedUsageLastUsedAt: beforeTouchLastUsed}); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("delete with pre-touch usage = %v, want usage changed", err)
	}
	afterRejectedDelete, err := os.ReadFile(touchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterRejectedDelete, beforeTouchDelete) {
		t.Fatal("stale touch delete changed source file")
	}
	activityAt := time.Now().UTC().Add(time.Second)
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-000000000322', 'file-reflect-eligible', 'web', 'chat', false, $1, $2, $3::text, $1, $1)
	`, activityAt, agentID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteReflectOwnedUserAgentSkill(ctx, ReflectSkillDelete{ID: touchDelete.ID, UserID: userID, AgentID: agentID, ExpectedDigest: touchDelete.ContentDigest, ExpectedUsageLastUsedAt: touchedLastUsed}); err != nil {
		t.Fatalf("eligible Reflect delete: %v", err)
	}
	var retainedCount int
	var retainedLastUsed time.Time
	var retainedDigest string
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at,content_digest FROM skill_usage WHERE skill_id=$1`, touchDelete.ID).Scan(&retainedCount, &retainedLastUsed, &retainedDigest); err != nil {
		t.Fatal(err)
	}
	if retainedCount != touchedCount || !retainedLastUsed.Equal(touchedLastUsed) || retainedDigest != touchDelete.ContentDigest {
		t.Fatalf("usage after successful delete = count %d at %s digest %q, want %d at %s digest %q", retainedCount, retainedLastUsed, retainedDigest, touchedCount, touchedLastUsed, touchDelete.ContentDigest)
	}
	var touchDeleteHistory int
	var latestAction string
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, touchDelete.ID).Scan(&touchDeleteHistory); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT action FROM skill_changelog WHERE skill_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, touchDelete.ID).Scan(&latestAction); err != nil {
		t.Fatal(err)
	}
	if touchDeleteHistory != 2 || latestAction != "delete" {
		t.Fatalf("successful delete history = %d latest action %q, want 2/delete", touchDeleteHistory, latestAction)
	}
	if err := store.TouchReflectSkillRuntimeUseDigest(ctx, touchDelete.ID, userID, agentID, touchDelete.ContentDigest); err != nil {
		t.Fatalf("touch deleted Reflect skill: %v", err)
	}
	var afterDeleteTouchCount int
	var afterDeleteTouchLastUsed time.Time
	if err := db.QueryRow(ctx, `SELECT use_count,last_used_at FROM skill_usage WHERE skill_id=$1`, touchDelete.ID).Scan(&afterDeleteTouchCount, &afterDeleteTouchLastUsed); err != nil {
		t.Fatal(err)
	}
	if afterDeleteTouchCount != retainedCount || !afterDeleteTouchLastUsed.Equal(retainedLastUsed) {
		t.Fatalf("touch after delete changed usage = count %d at %s, want %d at %s", afterDeleteTouchCount, afterDeleteTouchLastUsed, retainedCount, retainedLastUsed)
	}

	var writer string
	if err := db.QueryRow(ctx, `SELECT writer FROM skill_changelog WHERE skill_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, created.ID).Scan(&writer); err != nil {
		t.Fatal(err)
	}
	if writer != ReflectSkillCreatedBy {
		t.Fatalf("create evidence writer = %q, want %q", writer, ReflectSkillCreatedBy)
	}

	manual, err := store.CreateManagedSkill(ctx, Skill{Scope: "user", UserID: userID, Name: "manual-user", Description: "manual user skill"}, map[string]string{MainFile: "# manual\n"})
	if err != nil {
		t.Fatalf("manual create: %v", err)
	}
	updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: manual.Skill.ID, UserID: userID, Scope: "user", ExpectedDigest: manual.Skill.ContentDigest,
		Files: map[string]string{MainFile: "# manual updated\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteManagedSkill(ctx, ManagedSkillDelete{ID: updated.Skill.ID, UserID: userID, Scope: "user", ExpectedDigest: updated.Skill.ContentDigest}); err != nil {
		t.Fatal(err)
	}
	var manualUsage, manualHistory int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_usage WHERE skill_id=$1`, manual.Skill.ID).Scan(&manualUsage); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, manual.Skill.ID).Scan(&manualHistory); err != nil {
		t.Fatal(err)
	}
	if manualUsage != 0 || manualHistory != 3 {
		t.Fatalf("manual evidence usage/history = %d/%d, want 0/3", manualUsage, manualHistory)
	}
}

func stringPtr(value string) *string { return &value }
