package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestLogicalSkillUsageAcceptsLegacyAndHomeOnlyRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	userID, agentID := seedSkillUsageOwners(t, ctx, db)
	legacyID := "legacy-skill-usage"
	if _, err := db.Exec(ctx, `
		INSERT INTO skill (id, scope, user_id, agent_id, name, description)
		VALUES ($1, 'user_agent', $2, $3, 'legacy-usage', 'legacy source')`, legacyID, userID, agentID); err != nil {
		t.Fatalf("seed legacy skill: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO skill_usage (skill_id, user_id, agent_id)
		VALUES ($1, $2, $3)`, legacyID, userID, agentID); err != nil {
		t.Fatalf("insert legacy source usage: %v", err)
	}

	digest := strings.Repeat("a", 64)
	homeID := canonicalUsageHomeID(userID, agentID, "home-only")
	if _, err := db.Exec(ctx, `
		INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest)
		VALUES ($1, $2, $3, 'user_agent', 'home-only', $4)`, homeID, userID, agentID, digest); err != nil {
		t.Fatalf("insert Home-only usage without skill row: %v", err)
	}
	var skillRows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill WHERE id = $1`, homeID).Scan(&skillRows); err != nil || skillRows != 0 {
		t.Fatalf("Home-only skill rows = %d, %v; want 0", skillRows, err)
	}

	_, err := db.Exec(ctx, `UPDATE skill_usage SET scope = 'user_agent' WHERE skill_id = $1`, legacyID)
	assertSkillUsageConstraint(t, err, "skill_usage_logical_fields_check")
	for _, statement := range []string{
		`INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest) VALUES ('bad-name', $1, $2, 'user_agent', 'Bad', '` + digest + `')`,
		`INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest) VALUES ('bad-digest', $1, $2, 'user_agent', 'bad-digest', 'ABC')`,
		`INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest) VALUES ('bad-scope', $1, $2, 'user', 'bad-scope', '` + digest + `')`,
	} {
		_, err := db.Exec(ctx, statement, userID, agentID)
		assertSkillUsageConstraint(t, err, "skill_usage_logical_fields_check")
	}

	unknownUser := uuid.NewString()
	_, err = db.Exec(ctx, `
		INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest)
		VALUES ('missing-user', $1, $2, 'user_agent', 'missing-user', $3)`, unknownUser, agentID, digest)
	assertSkillUsageConstraint(t, err, "skill_usage_user_id_fkey")
	_, err = db.Exec(ctx, `
		INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest)
		VALUES ('missing-agent', $1, 'missing-agent', 'user_agent', 'missing-agent', $2)`, userID, digest)
	assertSkillUsageConstraint(t, err, "skill_usage_agent_id_fkey")
}

func TestLogicalSkillUsageDownKeepsHomeRowsAndRestoresUnvalidatedLegacyFK(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	userID, agentID := seedSkillUsageOwners(t, ctx, db)
	homeID := canonicalUsageHomeID(userID, agentID, "down-home")
	if _, err := db.Exec(ctx, `
		INSERT INTO skill_usage (skill_id, user_id, agent_id, scope, name, last_content_digest)
		VALUES ($1, $2, $3, 'user_agent', 'down-home', $4)`, homeID, userID, agentID, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed Home-only usage: %v", err)
	}

	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(db)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, sequentialAnchor+1); err != nil {
		t.Fatalf("down logical usage migration: %v", err)
	}
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_usage WHERE skill_id = $1`, homeID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("Home usage after Down = %d, %v; want 1", rows, err)
	}
	var validated bool
	if err := db.QueryRow(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conrelid = 'skill_usage'::regclass
		  AND conname = 'skill_usage_skill_id_fkey'`).Scan(&validated); err != nil {
		t.Fatalf("read restored legacy FK: %v", err)
	}
	if validated {
		t.Fatal("restored legacy skill_usage FK is validated; Home IDs make that rollback unsafe")
	}
}

func seedSkillUsageOwners(t *testing.T, ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
},
) (string, string) {
	t.Helper()
	userID, agentID := uuid.NewString(), "logical-usage-agent"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, userID, userID+"@test.invalid"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, $1, '/tmp')`, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return userID, agentID
}

func canonicalUsageHomeID(userID, agentID, name string) string {
	return fmt.Sprintf("skill/v1/10:user_agent%d:%s%d:%s%d:%s", len(userID), userID, len(agentID), agentID, len(name), name)
}

func assertSkillUsageConstraint(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != want {
		t.Fatalf("constraint error = %v, want %q", err, want)
	}
}
