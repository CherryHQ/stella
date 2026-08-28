package db

import (
	"context"
	"testing"
)

const (
	memoryOverridesBeforeMigration = 90000000000027
	memoryOverridesMigration       = 90000000000028
)

// memoryToolMatrix is the complete old -> new mapping the migration implements,
// spelled out independently of the SQL. A pair that disappears from the CTE
// silently re-enables recall somebody had switched off, and a pair that appears
// in the CTE but not here disables one nobody asked to lose — the exact row-set
// comparison below catches both directions.
var memoryToolMatrix = map[string][]string{
	"memory": {"memory_read", "memory_search"},
}

// memoryMatrixPairs pins what the matrix is worth in old/new pairs, so the
// family dropping out of the table fails rather than quietly shrinking the test.
const memoryMatrixPairs = 2

// The retired name has to land on exactly its replacements, and on nothing
// else. It migrates against a fresh database and the whole resulting row set is
// compared for equality, so one extra mapping in the CTE fails here even though
// every assertion about the intended names still passes.
func TestSplitMemoryOverridesMigratesTheWholeNameMatrix(t *testing.T) {
	pairs := 0
	for _, newNames := range memoryToolMatrix {
		pairs += len(newNames)
	}
	if pairs != memoryMatrixPairs {
		t.Fatalf("matrix holds %d old/new pairs, want %d", pairs, memoryMatrixPairs)
	}

	for oldName, newNames := range memoryToolMatrix {
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

				if _, err := provider.DownTo(ctx, memoryOverridesBeforeMigration); err != nil {
					t.Fatalf("restore pre-split schema: %v", err)
				}
				seedToolOverride(t, db, oldName, enabled)

				if _, err := provider.UpTo(ctx, memoryOverridesMigration); err != nil {
					t.Fatalf("migrate tool overrides: %v", err)
				}

				// Expand, not replace: the old row stays so a rollback has
				// something to fold back into.
				want := map[string]bool{oldName: enabled}
				for _, newName := range newNames {
					want[newName] = enabled
				}
				assertExactSystemOverrides(t, db, want)
			})
		}
	}
}

// The matrix above covers one old row at a time. These are the shapes where two
// sources reach the same new name, or where scope has to keep them apart.
func TestSplitMemoryOverridesUpMergesConflictingSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantUser   map[string]bool
	}{
		{
			name:       "an incoming union deny beats an existing enable on the new name",
			seed:       []overrideSeed{{tool: "memory"}, {tool: "memory_search", enabled: true}},
			wantSystem: map[string]bool{"memory_read": false, "memory_search": false},
		},
		{
			name:       "two enables stay enabled",
			seed:       []overrideSeed{{tool: "memory", enabled: true}, {tool: "memory_read", enabled: true}},
			wantSystem: map[string]bool{"memory_read": true, "memory_search": true},
		},
		{
			// Scope is part of the key: the fan-out must not leak one user's
			// decision onto another scope.
			name:       "a system deny does not reach a user who allowed it",
			seed:       []overrideSeed{{tool: "memory"}, {tool: "memory", enabled: true, user: true}},
			wantSystem: map[string]bool{"memory_search": false},
			wantUser:   map[string]bool{"memory_search": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.DownTo(ctx, memoryOverridesBeforeMigration); err != nil {
				t.Fatalf("restore pre-split schema: %v", err)
			}
			userID := seedOverrides(t, db, tc.seed)

			if _, err := provider.UpTo(ctx, memoryOverridesMigration); err != nil {
				t.Fatalf("migrate tool overrides: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			assertOverrides(t, db, "user", userID, tc.wantUser)
		})
	}
}

// The rollback has to survive an operator who had already written a row under
// the old union name: folding must AND into it, not overwrite it.
func TestSplitMemoryOverridesDownFoldsActionsBackWithBoolAnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seed       []overrideSeed
		wantSystem map[string]bool
		wantAbsent []string
	}{
		{
			name: "one disabled tool disables the union it folds into",
			seed: []overrideSeed{
				{tool: "memory", enabled: true},
				{tool: "memory_search", enabled: true},
				{tool: "memory_read"},
			},
			wantSystem: map[string]bool{"memory": false},
			wantAbsent: []string{"memory_read", "memory_search"},
		},
		{
			name:       "both enabled fold back to an enabled union",
			seed:       []overrideSeed{{tool: "memory_read", enabled: true}, {tool: "memory_search", enabled: true}},
			wantSystem: map[string]bool{"memory": true},
			wantAbsent: []string{"memory_read", "memory_search"},
		},
		{
			name:       "a mixed family folds deny-wins even with no prior union row",
			seed:       []overrideSeed{{tool: "memory_read"}, {tool: "memory_search", enabled: true}},
			wantSystem: map[string]bool{"memory": false},
			wantAbsent: []string{"memory_read", "memory_search"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			provider, closeProvider := reflectWatermarkProvider(t, db)
			defer closeProvider()
			ctx := context.Background()

			if _, err := provider.UpTo(ctx, memoryOverridesMigration); err != nil {
				t.Fatalf("migrate up: %v", err)
			}
			seedOverrides(t, db, tc.seed)

			if _, err := provider.DownTo(ctx, memoryOverridesBeforeMigration); err != nil {
				t.Fatalf("migrate down: %v", err)
			}

			assertOverrides(t, db, "system", "", tc.wantSystem)
			for _, name := range tc.wantAbsent {
				assertToolOverrideMissing(t, db, name)
			}
		})
	}
}
