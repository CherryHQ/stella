package db

import (
	"context"
	"fmt"
	"maps"
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

// splitToolMatrix is the complete old -> new mapping the migration implements,
// spelled out independently of the SQL. A pair that disappears from the CTE
// silently re-enables a capability somebody had switched off, and a pair that
// appears in the CTE but not here disables one nobody asked to lose — the exact
// row-set comparison below catches both directions.
var splitToolMatrix = map[string][]string{
	"goal": {"goal_cancel", "goal_create", "goal_get", "goal_list"},
	"scheduler": {
		"scheduler_job_create", "scheduler_job_delete", "scheduler_job_get",
		"scheduler_job_list", "scheduler_job_pause", "scheduler_job_resume",
		"scheduler_job_update",
	},
	"workflow": {"workflow_get", "workflow_list", "workflow_run", "workflow_save"},
	"oauth":    {"oauth_connect", "oauth_disconnect", "oauth_flow_status", "oauth_list"},
	"email":    {"email_account_list", "email_message_list", "email_message_read", "email_message_send"},
	"share":    {"share_create_article", "share_create_artifact", "share_list", "share_revoke"},
	"vault":    {"vault_secret_delete", "vault_secret_list", "vault_secret_set"},
	// Exact renames inside the already-split recally family.
	"recally_get_article":   {"recally_article_get"},
	"recally_list_articles": {"recally_article_list"},
	"recally_save_article":  {"recally_article_save"},
	"recally_digest":        {"recally_digest_get"},
	// The pre-#1171 union, which was split without a migration.
	"recally": recallyFinalNames,
}

// splitToolMatrixPairs is what the matrix is worth in old/new pairs. Pinning it
// makes a whole family dropping out of the table a failure rather than a
// quietly smaller test.
const splitToolMatrixPairs = 46

// Every retired name has to land on exactly its replacements, and on nothing
// else. Each old name migrates alone against a fresh database and the whole
// resulting row set is compared for equality, so one extra mapping in the CTE
// fails here even though every assertion about the intended names still passes.
func TestSplitToolOverridesMigratesTheWholeNameMatrix(t *testing.T) {
	oldNames := make([]string, 0, len(splitToolMatrix))
	pairs := 0
	for oldName, newNames := range splitToolMatrix {
		oldNames = append(oldNames, oldName)
		pairs += len(newNames)
	}
	sort.Strings(oldNames)
	if pairs != splitToolMatrixPairs {
		t.Fatalf("matrix holds %d old/new pairs, want %d", pairs, splitToolMatrixPairs)
	}

	for _, oldName := range oldNames {
		for _, enabled := range []bool{false, true} {
			name := oldName + "=false"
			if enabled {
				name = oldName + "=true"
			}
			t.Run(name, func(t *testing.T) {
				db := newTestDB(t)
				provider, closeProvider := reflectWatermarkProvider(t, db)
				defer closeProvider()
				ctx := context.Background()

				if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
					t.Fatalf("restore pre-split schema: %v", err)
				}
				seedToolOverride(t, db, oldName, enabled)

				if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
					t.Fatalf("migrate tool overrides: %v", err)
				}

				// Expand, not replace: the old row stays so a rollback has
				// something to fold back into.
				want := map[string]bool{oldName: enabled}
				for _, newName := range splitToolMatrix[oldName] {
					want[newName] = enabled
				}
				assertExactSystemOverrides(t, db, want)
			})
		}
	}
}

// The matrix above covers one old row at a time. These are the shapes where two
// sources reach the same new name: the merge is deny-wins, and "deny wins except
// in this one shape" is the failure mode worth catching.
func TestSplitToolOverridesUpMergesConflictingSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantUser   map[string]bool
		wantAbsent []string
	}{
		{
			name: "an incoming union deny beats an existing enable on the new name",
			seed: []overrideSeed{{tool: "goal"}, {tool: "goal_get", enabled: true}},
			wantSystem: map[string]bool{
				"goal_cancel": false, "goal_create": false, "goal_get": false, "goal_list": false,
			},
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

// assertExactSystemOverrides compares the whole system-scoped table, not just
// the names the caller thought to name. An extra row is as much a bug as a
// missing one: it disables a capability nobody asked to lose.
func assertExactSystemOverrides(t *testing.T, db *pgxpool.Pool, want map[string]bool) {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		SELECT tool_name, enabled FROM tool_override WHERE scope = 'system'
	`)
	if err != nil {
		t.Fatalf("read system overrides: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			t.Fatalf("scan system override: %v", err)
		}
		got[name] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read system overrides: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("system tool_override rows = %v, want %v", sortedPairs(got), sortedPairs(want))
	}
}

func sortedPairs(rows map[string]bool) []string {
	out := make([]string, 0, len(rows))
	for name, enabled := range rows {
		out = append(out, fmt.Sprintf("%s=%v", name, enabled))
	}
	sort.Strings(out)
	return out
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
