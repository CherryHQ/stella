package db

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	splitToolOverridesBeforeMigration = 90000000000025
	splitToolOverridesMigration       = 90000000000026
)

// recallyFinalNames is the whole family after the split. The pre-#1171 union
// row has to reach every one of them, so the list is spelled out rather than
// derived: a name that silently drops out of the mapping is exactly the bug.
var recallyFinalNames = []string{
	"recally_article_get", "recally_article_list", "recally_article_save",
	"recally_digest_get", "recally_digest_save",
	"recally_entry_add", "recally_entry_list", "recally_entry_update",
	"recally_feed_add", "recally_feed_list", "recally_feed_poll", "recally_feed_remove",
}

// overrideSeed is one tool_override row as it exists before the migration runs.
type overrideSeed struct {
	tool    string
	enabled bool
	// user scopes the row to the seeded test user instead of the system.
	user bool
}

func allOf(enabled bool, names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = enabled
	}
	return out
}

// A tool_override row is keyed by name, so the split would silently re-enable
// every capability an operator or a user had switched off under a union name.
// Each case pins the exact resulting rows: the merge is deny-wins, and "deny
// wins except in this one shape" is the failure mode worth catching.
func TestSplitToolOverridesUpMigratesEveryRetiredName(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantUser   map[string]bool
		wantAbsent []string
	}{
		{
			name: "a union deny fans out to every action in the family",
			seed: []overrideSeed{{tool: "goal"}},
			wantSystem: map[string]bool{
				"goal_cancel": false, "goal_create": false, "goal_get": false, "goal_list": false,
				// Expand, not replace: a rollback needs the old row to fold into.
				"goal": false,
			},
			// A family nobody touched gains nothing.
			wantAbsent: []string{"workflow_run", "scheduler_job_get"},
		},
		{
			name:       "a union enable fans out as an enable",
			seed:       []overrideSeed{{tool: "vault", enabled: true}},
			wantSystem: allOf(true, "vault_secret_delete", "vault_secret_list", "vault_secret_set", "vault"),
		},
		{
			name: "a renamed action carries its row to the new name",
			seed: []overrideSeed{{tool: "recally_digest"}},
			wantSystem: map[string]bool{
				"recally_digest_get": false,
				"recally_digest":     false,
			},
			// The rename moves one row, not the family.
			wantAbsent: []string{"recally_digest_save", "recally_article_get"},
		},
		{
			name: "an incoming union deny beats an existing enable on the new name",
			seed: []overrideSeed{{tool: "goal"}, {tool: "goal_get", enabled: true}},
			wantSystem: map[string]bool{
				"goal_cancel": false, "goal_create": false, "goal_get": false, "goal_list": false,
			},
		},
		{
			name:       "the pre-split recally union denies all twelve final names",
			seed:       []overrideSeed{{tool: "recally"}},
			wantSystem: allOf(false, recallyFinalNames...),
		},
		{
			// Two old names reaching one new name in the same statement is the
			// case ON CONFLICT DO UPDATE cannot handle without the pre-fold.
			name:       "two old names reaching one new name merge deny-wins",
			seed:       []overrideSeed{{tool: "recally"}, {tool: "recally_save_article", enabled: true}},
			wantSystem: map[string]bool{"recally_article_save": false},
		},
		{
			name:       "two enables stay enabled",
			seed:       []overrideSeed{{tool: "share", enabled: true}, {tool: "share_list", enabled: true}},
			wantSystem: map[string]bool{"share_list": true, "share_revoke": true},
		},
		{
			// Scope is part of the key: the fan-out must not leak one user's
			// decision onto another scope.
			name:       "a system deny does not reach a user who allowed it",
			seed:       []overrideSeed{{tool: "email"}, {tool: "email", enabled: true, user: true}},
			wantSystem: map[string]bool{"email_message_send": false},
			wantUser:   map[string]bool{"email_message_send": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
				t.Fatalf("restore pre-split schema: %v", err)
			}
			userID := seedOverrides(t, db, tc.seed)

			if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
				t.Fatalf("migrate tool overrides: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			assertOverrides(t, db, "user", userID, tc.wantUser)
			for _, name := range tc.wantAbsent {
				assertToolOverrideMissing(t, db, name)
			}
		})
	}
}

// The rollback has to survive an operator who had already written a row under
// the old union name: folding must AND into it, not overwrite it.
func TestSplitToolOverridesDownFoldsActionsBackWithBoolAnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantAbsent []string
	}{
		{
			name: "one disabled action disables the union it folds into",
			seed: []overrideSeed{
				{tool: "goal", enabled: true},
				{tool: "goal_cancel", enabled: true},
				{tool: "goal_create", enabled: true},
				{tool: "goal_get"},
				{tool: "goal_list", enabled: true},
			},
			wantSystem: map[string]bool{"goal": false},
			wantAbsent: []string{"goal_cancel", "goal_create", "goal_get", "goal_list"},
		},
		{
			name:       "all actions enabled fold back to an enabled union",
			seed:       []overrideSeed{{tool: "workflow_run", enabled: true}, {tool: "workflow_list", enabled: true}},
			wantSystem: map[string]bool{"workflow": true},
			wantAbsent: []string{"workflow_run", "workflow_list"},
		},
		{
			name:       "a mixed family folds deny-wins even with no prior union row",
			seed:       []overrideSeed{{tool: "workflow_run"}, {tool: "workflow_list", enabled: true}},
			wantSystem: map[string]bool{"workflow": false},
			wantAbsent: []string{"workflow_run", "workflow_list"},
		},
		{
			// The eight recally names that predate this migration must survive
			// the rollback, and the fold must not mint a "recally" union row
			// that a re-run of up would fan out as a deny across all twelve.
			name: "names older than the migration survive and the recally union is not resurrected",
			seed: []overrideSeed{{tool: "recally_feed_add", enabled: true}, {tool: "recally_article_get"}},
			wantSystem: map[string]bool{
				"recally_get_article": false,
				"recally_feed_add":    true,
			},
			wantAbsent: []string{"recally_article_get", "recally"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
				t.Fatalf("migrate up: %v", err)
			}
			seedOverrides(t, db, tc.seed)

			if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
				t.Fatalf("migrate down: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			for _, name := range tc.wantAbsent {
				assertToolOverrideMissing(t, db, name)
			}
		})
	}
}

// seedOverrides writes the rows and returns the user id it created, or "" when
// no case needed a user-scoped row.
func seedOverrides(t *testing.T, db *pgxpool.Pool, seed []overrideSeed) string {
	t.Helper()
	var userID string
	for _, row := range seed {
		if !row.user {
			seedToolOverride(t, db, row.tool, row.enabled)
			continue
		}
		if userID == "" {
			userID = seedToolOverrideUser(t, db)
		}
		seedUserToolOverride(t, db, row.tool, userID, row.enabled)
	}
	return userID
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

// assertOverrides checks one scope's expected rows. A missing row fails as
// loudly as a wrong value: "no row" means the capability came back on.
func assertOverrides(t *testing.T, db *pgxpool.Pool, scope, userID string, want map[string]bool) {
	t.Helper()
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var got bool
		var err error
		if scope == "user" {
			err = db.QueryRow(context.Background(), `
				SELECT enabled FROM tool_override WHERE tool_name = $1 AND scope = 'user' AND user_id = $2
			`, name, userID).Scan(&got)
		} else {
			err = db.QueryRow(context.Background(), `
				SELECT enabled FROM tool_override WHERE tool_name = $1 AND scope = 'system'
			`, name).Scan(&got)
		}
		if err != nil {
			t.Errorf("read %s override %q: %v", scope, name, err)
			continue
		}
		if got != want[name] {
			t.Errorf("tool_override[%q, %s].enabled = %v, want %v", name, scope, got, want[name])
		}
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
