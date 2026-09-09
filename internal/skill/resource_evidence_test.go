package skill

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestLegacySkillResourceEvidenceUsesOneLogicalUsageAndPreservesHistory(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	const userID = "00000000-0000-4000-8000-000000000481"
	const agentID = "skill-resource-agent"
	const legacyID = "legacy-skill-resource"
	const resourceID = "file:WyJ1c2VyX2FnZW50IiwiMDAwMDAwMDAtMDAwMC00MDAwLTgwMDAtMDAwMDAwMDAwNDgxIiwic2tpbGwtcmVzb3VyY2UtYWdlbnQiLCJkb2NzIl0"
	const collisionSourceID = "legacy-skill-changelog-source"
	const collisionLegacyID = "legacy-skill-changelog-collision"
	const collisionResourceID = "file:WyJ1c2VyX2FnZW50IiwiMDAwMDAwMDAtMDAwMC00MDAwLTgwMDAtMDAwMDAwMDAwNDgxIiwic2tpbGwtcmVzb3VyY2UtYWdlbnQiLCJjb2xsaXNpb24"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC().Add(-time.Minute)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO auth_user(id,email) VALUES($1,'resource-evidence@example.invalid')`, userID)
	exec(`INSERT INTO agent(id,name,workspace) VALUES($1,'Resource evidence','')`, agentID)
	exec(`INSERT INTO skill(id,scope,user_id,agent_id,name,description,metadata) VALUES($1,'user_agent',$2,$3,'docs','legacy','{"created_by":"reflect"}')`, legacyID, userID, agentID)
	exec(`INSERT INTO skill_usage(skill_id,user_id,agent_id,use_count,last_used_at,content_digest) VALUES($1,$2,$3,7,$4,$5)`, legacyID, userID, agentID, now, digest)
	exec(`INSERT INTO skill_changelog(skill_id,user_id,agent_id,scope,action,version_after,content_digest,writer,metadata,created_at) VALUES($1,$2,$3,'user_agent','create',1,$4,'reflect','{}',$5)`, legacyID, userID, agentID, digest, now)
	exec(`INSERT INTO skill(id,scope,user_id,agent_id,name,description,metadata) VALUES($1,'user_agent',$2,$3,'collision','legacy','{"created_by":"reflect"}')`, collisionLegacyID, userID, agentID)
	exec(`INSERT INTO skill_changelog(skill_id,resource_id,user_id,agent_id,scope,action,version_after,content_digest,writer,metadata,created_at) VALUES($1,$2,$3,$4,'user_agent','create',1,$5,'reflect','{}',$6)`, collisionSourceID, collisionResourceID, userID, agentID, digest, now)

	q := sqlc.New(db)
	collision, err := q.LinkLegacySkillResource(ctx, sqlc.LinkLegacySkillResourceParams{
		LegacySkillID: collisionLegacyID,
		Scope:         "user_agent",
		UserID:        pgtype.Text{String: userID, Valid: true},
		AgentID:       pgtype.Text{String: agentID, Valid: true},
		Name:          "collision",
		ResourceID:    collisionResourceID,
	})
	if err != nil || !collision.LegacyMatch || !collision.HasConflict || collision.UsageLinked != 0 || collision.ChangelogLinked != 0 {
		t.Fatalf("changelog collision result = %+v, err=%v", collision, err)
	}
	linked, err := q.LinkLegacySkillResource(ctx, sqlc.LinkLegacySkillResourceParams{
		LegacySkillID: legacyID,
		Scope:         "user_agent",
		UserID:        pgtype.Text{String: userID, Valid: true},
		AgentID:       pgtype.Text{String: agentID, Valid: true},
		Name:          "docs",
		ResourceID:    resourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !linked.LegacyMatch || linked.HasConflict || linked.UsageLinked != 1 || linked.ChangelogLinked != 1 {
		t.Fatalf("link result = %+v", linked)
	}
	retry, err := q.LinkLegacySkillResource(ctx, sqlc.LinkLegacySkillResourceParams{
		LegacySkillID: legacyID, Scope: "user_agent", UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Name: "docs", ResourceID: resourceID,
	})
	if err != nil || !retry.LegacyMatch || retry.HasConflict || retry.UsageLinked != 0 || retry.ChangelogLinked != 0 {
		t.Fatalf("idempotent link result = %+v, err=%v", retry, err)
	}

	if err := q.UpsertSkillUsageOnReflectCreate(ctx, sqlc.UpsertSkillUsageOnReflectCreateParams{SkillID: resourceID, UserID: userID, AgentID: agentID, ContentDigest: pgtype.Text{String: digest, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	usage, err := q.GetSkillUsageForUpdate(ctx, sqlc.GetSkillUsageForUpdateParams{SkillID: resourceID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if usage.SkillID != legacyID || !usage.ResourceID.Valid || usage.ResourceID.String != resourceID {
		t.Fatalf("usage identity split: %+v", usage)
	}
	var usageRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_usage WHERE coalesce(resource_id,skill_id)=$1`, resourceID).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 1 {
		t.Fatalf("logical usage rows = %d, want 1", usageRows)
	}
	store := NewFileStore(db, nil)
	if err := store.TouchReflectSkillRuntimeUseDigest(ctx, resourceID, userID, agentID, digest); err != nil {
		t.Fatal(err)
	}
	if err := q.RefreshSkillUsageOnReflectPatch(ctx, sqlc.RefreshSkillUsageOnReflectPatchParams{SkillID: resourceID, UserID: userID, AgentID: agentID, ContentDigest: pgtype.Text{String: digest, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*), use_count FROM skill_usage WHERE coalesce(resource_id,skill_id)=$1 GROUP BY use_count`, resourceID).Scan(&usageRows, new(int64)); err != nil {
		t.Fatal(err)
	}
	if usageRows != 1 {
		t.Fatalf("logical usage rows after touch/patch = %d, want 1", usageRows)
	}

	latest, err := q.GetLatestSkillChangelogBySkill(ctx, resourceID)
	if err != nil || latest.SkillID != legacyID || latest.ResourceID.String != resourceID {
		t.Fatalf("historical changelog = %+v, err=%v", latest, err)
	}
	if _, err := q.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{SkillID: resourceID, UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}, Scope: "user_agent", Action: "patch", VersionAfter: 2, ContentDigest: pgtype.Text{String: digest, Valid: true}, Writer: "reflect", Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListSkillChangelogBySkill(ctx, sqlc.ListSkillChangelogBySkillParams{SkillID: resourceID, LimitCount: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("logical changelog rows = %d, err=%v", len(rows), err)
	}
	if rows[0].SkillID != resourceID || rows[1].SkillID != legacyID {
		t.Fatalf("new and old changelog IDs = %q, %q", rows[0].SkillID, rows[1].SkillID)
	}
}
