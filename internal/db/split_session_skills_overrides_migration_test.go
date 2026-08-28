package db

import (
	"context"
	"sort"
	"testing"
)

const (
	sessionSkillsOverridesBeforeMigration = 90000000000026
	sessionSkillsOverridesMigration       = 90000000000027
)

// sessionSkillsToolMatrix is the complete old -> new mapping the migration
// implements, spelled out independently of the SQL. A pair that disappears from
// the CTE silently re-enables a capability somebody had switched off, and a pair
// that appears in the CTE but not here disables one nobody asked to lose — the
// exact row-set comparison below catches both directions.
var sessionSkillsToolMatrix = map[string][]string{
	"session": {"session_create", "session_get", "session_list", "session_send"},
	// The union was named `skills`; the family is `skill`.
	"skills": {"skill_installed_search", "skill_load"},
}

// sessionSkillsMatrixPairs pins what the matrix is worth in old/new pairs, so a
// whole family dropping out of the table fails rather than quietly shrinking the
// test.
const sessionSkillsMatrixPairs = 6

// Every retired name has to land on exactly its replacements, and on nothing
// else. Each old name migrates alone against a fresh database and the whole
// resulting row set is compared for equality, so one extra mapping in the CTE
// fails here even though every assertion about the intended names still passes.
func TestSplitSessionAndSkillsOverridesMigratesTheWholeNameMatrix(t *testing.T) {
	oldNames := make([]string, 0, len(sessionSkillsToolMatrix))
	pairs := 0
	for oldName, newNames := range sessionSkillsToolMatrix {
		oldNames = append(oldNames, oldName)
		pairs += len(newNames)
	}
	sort.Strings(oldNames)
	if pairs != sessionSkillsMatrixPairs {
		t.Fatalf("matrix holds %d old/new pairs, want %d", pairs, sessionSkillsMatrixPairs)
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

				if _, err := provider.DownTo(ctx, sessionSkillsOverridesBeforeMigration); err != nil {
					t.Fatalf("restore pre-split schema: %v", err)
				}
				seedToolOverride(t, db, oldName, enabled)

				if _, err := provider.UpTo(ctx, sessionSkillsOverridesMigration); err != nil {
					t.Fatalf("migrate tool overrides: %v", err)
				}

				// Expand, not replace: the old row stays so a rollback has
				// something to fold back into.
				want := map[string]bool{oldName: enabled}
				for _, newName := range sessionSkillsToolMatrix[oldName] {
					want[newName] = enabled
				}
				assertExactSystemOverrides(t, db, want)
			})
		}
	}
}

// The matrix above covers one old row at a time. These are the shapes where two
// sources reach the same new name, or where scope has to keep them apart.
func TestSplitSessionAndSkillsOverridesUpMergesConflictingSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantUser   map[string]bool
	}{
		{
			name: "an incoming union deny beats an existing enable on the new name",
			seed: []overrideSeed{{tool: "session"}, {tool: "session_send", enabled: true}},
			wantSystem: map[string]bool{
				"session_create": false, "session_get": false,
				"session_list": false, "session_send": false,
			},
		},
		{
			name:       "two enables stay enabled",
			seed:       []overrideSeed{{tool: "skills", enabled: true}, {tool: "skill_load", enabled: true}},
			wantSystem: map[string]bool{"skill_installed_search": true, "skill_load": true},
		},
		{
			// Scope is part of the key: the fan-out must not leak one user's
			// decision onto another scope.
			name:       "a system deny does not reach a user who allowed it",
			seed:       []overrideSeed{{tool: "session"}, {tool: "session", enabled: true, user: true}},
			wantSystem: map[string]bool{"session_send": false},
			wantUser:   map[string]bool{"session_send": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.DownTo(ctx, sessionSkillsOverridesBeforeMigration); err != nil {
				t.Fatalf("restore pre-split schema: %v", err)
			}
			userID := seedOverrides(t, db, tc.seed)

			if _, err := provider.UpTo(ctx, sessionSkillsOverridesMigration); err != nil {
				t.Fatalf("migrate tool overrides: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			assertOverrides(t, db, "user", userID, tc.wantUser)
		})
	}
}

// The rollback has to survive an operator who had already written a row under
// the old union name: folding must AND into it, not overwrite it.
func TestSplitSessionAndSkillsOverridesDownFoldsActionsBackWithBoolAnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantAbsent []string
	}{
		{
			name: "one disabled action disables the union it folds into",
			seed: []overrideSeed{
				{tool: "session", enabled: true},
				{tool: "session_create", enabled: true},
				{tool: "session_get", enabled: true},
				{tool: "session_list", enabled: true},
				{tool: "session_send"},
			},
			wantSystem: map[string]bool{"session": false},
			wantAbsent: []string{"session_create", "session_get", "session_list", "session_send"},
		},
		{
			name:       "all actions enabled fold back to an enabled union",
			seed:       []overrideSeed{{tool: "skill_load", enabled: true}, {tool: "skill_installed_search", enabled: true}},
			wantSystem: map[string]bool{"skills": true},
			wantAbsent: []string{"skill_load", "skill_installed_search"},
		},
		{
			name:       "a mixed family folds deny-wins even with no prior union row",
			seed:       []overrideSeed{{tool: "skill_load"}, {tool: "skill_installed_search", enabled: true}},
			wantSystem: map[string]bool{"skills": false},
			wantAbsent: []string{"skill_load", "skill_installed_search"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.UpTo(ctx, sessionSkillsOverridesMigration); err != nil {
				t.Fatalf("migrate up: %v", err)
			}
			seedOverrides(t, db, tc.seed)

			if _, err := provider.DownTo(ctx, sessionSkillsOverridesBeforeMigration); err != nil {
				t.Fatalf("migrate down: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			for _, name := range tc.wantAbsent {
				assertToolOverrideMissing(t, db, name)
			}
		})
	}
}
