package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	splitToolOverridesBeforeMigration = 90000000000025
	splitToolOverridesMigration       = 90000000000026
)

// A tool_override row is keyed by name, so the split would silently re-enable
// every capability an operator or user had switched off under a union name.
func TestSplitToolOverridesFansUnionRowsOutToActions(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
		t.Fatalf("restore pre-split schema: %v", err)
	}
	seedToolOverride(t, db, "scheduler", false)
	seedToolOverride(t, db, "vault", true)
	seedToolOverride(t, db, "recally_save_article", false)

	if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
		t.Fatalf("migrate tool overrides: %v", err)
	}

	for _, name := range []string{
		"scheduler_job_create", "scheduler_job_delete", "scheduler_job_get",
		"scheduler_job_list", "scheduler_job_pause", "scheduler_job_resume",
		"scheduler_job_update",
	} {
		assertToolOverride(t, db, name, false)
	}
	assertToolOverride(t, db, "vault_secret_delete", true)
	assertToolOverride(t, db, "vault_secret_list", true)
	assertToolOverride(t, db, "vault_secret_set", true)
	assertToolOverride(t, db, "recally_article_save", false)

	// Expand, not replace: a rollback needs the old rows to fold back into.
	assertToolOverride(t, db, "scheduler", false)
	assertToolOverride(t, db, "recally_save_article", false)

	// A family nobody touched gains nothing.
	assertToolOverrideMissing(t, db, "goal_create")
}

// Deny-wins is the whole point of the merge: a disabled capability must not come
// back on because a second source said it was fine.
func TestSplitToolOverridesMergesDenyWins(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
		t.Fatalf("restore pre-split schema: %v", err)
	}
	// The pre-#1171 union says no; the post-#1171 exact name says yes. Two old
	// names reaching one new name in the same statement is also the case
	// ON CONFLICT DO UPDATE cannot handle without the pre-fold.
	seedToolOverride(t, db, "recally", false)
	seedToolOverride(t, db, "recally_save_article", true)
	// An existing row for a new name loses to an incoming deny.
	seedToolOverride(t, db, "goal", false)
	seedToolOverride(t, db, "goal_list", true)
	// Two enables stay enabled.
	seedToolOverride(t, db, "share", true)
	seedToolOverride(t, db, "share_list", true)

	if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
		t.Fatalf("migrate tool overrides: %v", err)
	}

	assertToolOverride(t, db, "recally_article_save", false)
	assertToolOverride(t, db, "recally_feed_add", false)
	assertToolOverride(t, db, "goal_list", false)
	assertToolOverride(t, db, "goal_get", false)
	assertToolOverride(t, db, "share_list", true)
	assertToolOverride(t, db, "share_revoke", true)
}

// Scope is part of the key, so the fan-out must not leak one user's decision
// onto another user or onto the system default.
func TestSplitToolOverridesKeepsScopesApart(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
		t.Fatalf("restore pre-split schema: %v", err)
	}
	userID := seedToolOverrideUser(t, db)
	seedToolOverride(t, db, "email", false)
	seedUserToolOverride(t, db, "email", userID, true)

	if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
		t.Fatalf("migrate tool overrides: %v", err)
	}

	assertToolOverride(t, db, "email_message_send", false)
	assertScopedToolOverride(t, db, "email_message_send", userID, true)
}

func TestSplitToolOverridesDownFoldsActionsBackWithBoolAnd(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	seedToolOverride(t, db, "workflow_run", false)
	seedToolOverride(t, db, "workflow_list", true)
	// A recally name that predates this migration must survive the rollback:
	// the schema it belongs to predates the migration too.
	seedToolOverride(t, db, "recally_feed_add", true)
	seedToolOverride(t, db, "recally_article_get", false)

	if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
		t.Fatalf("migrate down: %v", err)
	}

	assertToolOverride(t, db, "workflow", false)
	assertToolOverrideMissing(t, db, "workflow_run")
	assertToolOverrideMissing(t, db, "workflow_list")
	assertToolOverride(t, db, "recally_get_article", false)
	assertToolOverrideMissing(t, db, "recally_article_get")
	assertToolOverride(t, db, "recally_feed_add", true)
	// The legacy union is not resurrected by the fold.
	assertToolOverrideMissing(t, db, "recally")
}

func seedToolOverride(t *testing.T, db *pgxpool.Pool, tool string, enabled bool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO tool_override (tool_name, scope, enabled) VALUES ($1, 'system', $2)
	`, tool, enabled); err != nil {
		t.Fatalf("seed tool override %q: %v", tool, err)
	}
}

func seedUserToolOverride(t *testing.T, db *pgxpool.Pool, tool, userID string, enabled bool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO tool_override (tool_name, scope, user_id, enabled) VALUES ($1, 'user', $2, $3)
	`, tool, userID, enabled); err != nil {
		t.Fatalf("seed user tool override %q: %v", tool, err)
	}
}

func seedToolOverrideUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO auth_user (email) VALUES ('split-override@test.invalid') RETURNING id
	`).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func assertToolOverride(t *testing.T, db *pgxpool.Pool, tool string, want bool) {
	t.Helper()
	var got bool
	if err := db.QueryRow(context.Background(), `
		SELECT enabled FROM tool_override WHERE tool_name = $1 AND scope = 'system'
	`, tool).Scan(&got); err != nil {
		t.Fatalf("read tool override %q: %v", tool, err)
	}
	if got != want {
		t.Errorf("tool_override[%q].enabled = %v, want %v", tool, got, want)
	}
}

func assertScopedToolOverride(t *testing.T, db *pgxpool.Pool, tool, userID string, want bool) {
	t.Helper()
	var got bool
	if err := db.QueryRow(context.Background(), `
		SELECT enabled FROM tool_override WHERE tool_name = $1 AND scope = 'user' AND user_id = $2
	`, tool, userID).Scan(&got); err != nil {
		t.Fatalf("read user tool override %q: %v", tool, err)
	}
	if got != want {
		t.Errorf("tool_override[%q, user].enabled = %v, want %v", tool, got, want)
	}
}

func assertToolOverrideMissing(t *testing.T, db *pgxpool.Pool, tool string) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM tool_override WHERE tool_name = $1)
	`, tool).Scan(&exists); err != nil {
		t.Fatalf("probe tool override %q: %v", tool, err)
	}
	if exists {
		t.Errorf("tool_override[%q] exists, want no row", tool)
	}
}
