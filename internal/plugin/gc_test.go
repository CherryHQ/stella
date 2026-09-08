package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestServiceCleanupRetiredOwnershipSharedRootsAndNameReuse(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owners := ContentOwnerSnapshot{}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{}, inlineBackendPolicyFence,
		WithContentStore(store), WithContentOwnerSnapshot(func(context.Context) (ContentOwnerSnapshot, error) {
			return owners, nil
		}))

	activeDigest := gcDigest('a')
	heldDigest := gcDigest('b')
	deletableDigest := gcDigest('c')
	oldOwnerDigest := gcDigest('d')
	orphanDigest := gcDigest('e')
	sharedDigest := gcDigest('6')
	for _, digest := range []string{activeDigest, heldDigest, deletableDigest, oldOwnerDigest, orphanDigest, sharedDigest} {
		if err := os.Mkdir(filepath.Join(store.root, digest), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	seedGCDefinition(t, db, "active", activeDigest, false)
	seedGCDefinition(t, db, "active-shared", sharedDigest, false)
	seedGCDefinition(t, db, "retired-shared", sharedDigest, true)
	seedGCDefinition(t, db, "held", heldDigest, true)
	deletable := seedGCDefinition(t, db, "deletable", deletableDigest, true)
	if _, err := db.Exec(t.Context(), `INSERT INTO tool_override (scope, enabled, plugin_id, local_tool_name) VALUES ('system', true, $1, 'tool')`, deletable); err != nil {
		t.Fatal(err)
	}
	owners = ContentOwnerSnapshot{PluginIDs: []string{"held"}, Digests: []string{"sha256:" + oldOwnerDigest}}
	if err := service.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertGCExists(t, store.root, activeDigest, heldDigest, oldOwnerDigest, sharedDigest)
	assertGCAbsent(t, store.root, deletableDigest, orphanDigest)
	assertGCRowCounts(t, db, "held", 1, 1)
	assertGCRowCounts(t, db, "retired-shared", 0, 0)
	assertGCRowCounts(t, db, "deletable", 0, 0)
	var policies int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM tool_override WHERE plugin_id = 'deletable'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 0 {
		t.Fatalf("deletable policies = %d, want 0", policies)
	}

	owners = ContentOwnerSnapshot{}
	if err := service.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertGCAbsent(t, store.root, heldDigest)
	assertGCRowCounts(t, db, "held", 0, 0)
	if _, err := db.Exec(t.Context(), `INSERT INTO plugin_definition (id, display_name, source, spec, revision) VALUES ('held', 'reused', 'custom', '{}'::jsonb, 1)`); err != nil {
		t.Fatalf("retired package name was not released: %v", err)
	}
}

func TestServiceCleanupCASChangePreservesChildrenAndFreshStoreFinalizesMissingBytes(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{}, inlineBackendPolicyFence,
		WithContentStore(store), WithContentOwnerSnapshot(func(context.Context) (ContentOwnerSnapshot, error) {
			return ContentOwnerSnapshot{}, nil
		}))
	casDigest := gcDigest('f')
	casID := seedGCDefinition(t, db, "cas-change", casDigest, true)
	if _, err := db.Exec(t.Context(), `INSERT INTO tool_override (scope, enabled, plugin_id, local_tool_name) VALUES ('system', true, $1, 'tool')`, casID); err != nil {
		t.Fatal(err)
	}
	plan, err := service.collectRetiredCleanup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE plugin_definition SET revision = revision + 1 WHERE id = $1`, casID); err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeRetiredCleanup(t.Context(), plan, map[string]bool{casDigest: true}); err != nil {
		t.Fatalf("CAS-changed definition cleanup: %v", err)
	}
	assertGCRowCounts(t, db, casID, 1, 1)
	var policies int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM tool_override WHERE plugin_id = $1`, casID).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 1 {
		t.Fatalf("CAS-changed policies = %d, want 1", policies)
	}

	missingDigest := gcDigest('1')
	missingID := seedGCDefinition(t, db, "missing-bytes", missingDigest, true)
	// The directory never existed. A fresh store after restart must use the
	// absence of both live and quarantine roots as removal evidence.
	fresh, err := NewContentStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	service.contentStore = fresh
	if err := service.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertGCRowCounts(t, db, missingID, 0, 0)
}

func TestServiceCleanupFailsClosedOnInvalidCurrentContentReference(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{}, inlineBackendPolicyFence,
		WithContentStore(store), WithContentOwnerSnapshot(func(context.Context) (ContentOwnerSnapshot, error) {
			return ContentOwnerSnapshot{}, nil
		}))
	orphan := gcDigest('2')
	if err := os.Mkdir(filepath.Join(store.root, orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO plugin_definition (id, display_name, source, spec, revision) VALUES ('bad-current', 'bad', 'custom', '{"content":{"digest":"not-a-digest"}}'::jsonb, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := service.Cleanup(t.Context()); err == nil {
		t.Fatal("cleanup accepted an invalid current content reference")
	}
	assertGCExists(t, store.root, orphan)
}

func seedGCDefinition(t *testing.T, db *pgxpool.Pool, id, digest string, retired bool) string {
	t.Helper()
	encoded, err := json.Marshal(ResourcePayload{Origin: "package", Content: &ContentReference{Digest: "sha256:" + digest}})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := PublishDefinitionSpec(encoded)
	if err != nil {
		t.Fatal(err)
	}
	retiredSQL := "NULL"
	if retired {
		retiredSQL = "now()"
	}
	if _, err := db.Exec(context.Background(), fmt.Sprintf(`INSERT INTO plugin_definition (id, display_name, source, spec, revision, retired_at) VALUES ($1, $2, 'custom', $3, 1, %s)`, retiredSQL), id, id, spec); err != nil {
		t.Fatal(err)
	}
	if retired {
		if _, err := db.Exec(context.Background(), `INSERT INTO plugin_config (plugin_id, scope, enabled, config, credential_refs, revision) VALUES ($1, 'system', false, '{}'::jsonb, '{}'::jsonb, 1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func assertGCRowCounts(t *testing.T, db *pgxpool.Pool, id string, definitions, configs int) {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM plugin_definition WHERE id = $1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != definitions {
		t.Fatalf("definition %s count = %d, want %d", id, count, definitions)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM plugin_config WHERE plugin_id = $1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != configs {
		t.Fatalf("config %s count = %d, want %d", id, count, configs)
	}
}

func assertGCExists(t *testing.T, root string, digests ...string) {
	t.Helper()
	for _, digest := range digests {
		if _, err := os.Stat(filepath.Join(root, digest)); err != nil {
			t.Fatalf("digest %s missing: %v", digest, err)
		}
	}
}

func assertGCAbsent(t *testing.T, root string, digests ...string) {
	t.Helper()
	for _, digest := range digests {
		if _, err := os.Stat(filepath.Join(root, digest)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("digest %s remains: %v", digest, err)
		}
	}
}

func gcDigest(ch byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = ch
	}
	return string(buf)
}
