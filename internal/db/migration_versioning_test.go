package db

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	legacyMigrationMax   = int64(20260804120000)
	legacyMigrationCount = 36
	sequentialAnchor     = int64(90000000000000)
)

var canonicalMigrationName = regexp.MustCompile(`^([1-9][0-9]*)_.+\.sql$`)

func TestEmbeddedMigrationVersioning(t *testing.T) {
	if err := validateMigrationVersioning(MigrationsFS); err != nil {
		t.Fatal(err)
	}

	entries, err := fs.ReadDir(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	legacyCount := 0
	for _, entry := range entries {
		match := canonicalMigrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			t.Fatalf("parse embedded migration %q: %v", entry.Name(), err)
		}
		if version < sequentialAnchor {
			legacyCount++
		}
	}
	// Timestamped migration history is frozen; every new migration must use the
	// contiguous sequential range at or above sequentialAnchor.
	if legacyCount != legacyMigrationCount {
		t.Fatalf("legacy migration count = %d, want frozen count %d", legacyCount, legacyMigrationCount)
	}
}

func TestValidateMigrationVersioning(t *testing.T) {
	t.Parallel()

	valid := migrationFS(
		"20260804120000_legacy.sql",
		"90000000000000_sequential_versioning.sql",
		"90000000000001_next.sql",
	)
	tests := []struct {
		name string
		fs   fs.FS
		want string
	}{
		{name: "valid", fs: valid},
		{
			name: "duplicate", fs: migrationFS(
				"20260804120000_legacy.sql",
				"90000000000000_sequential_versioning.sql",
				"90000000000001_first.sql",
				"90000000000001_second.sql",
			), want: "duplicate migration version 90000000000001",
		},
		{
			name: "gap", fs: migrationFS(
				"20260804120000_legacy.sql",
				"90000000000000_sequential_versioning.sql",
				"90000000000002_skipped_one.sql",
			), want: "expected 90000000000001",
		},
		{
			name: "post legacy timestamp", fs: migrationFS(
				"20260804120000_legacy.sql",
				"20260804120001_forbidden.sql",
				"90000000000000_sequential_versioning.sql",
			), want: "lies between legacy maximum",
		},
		{
			name: "legacy maximum changed", fs: migrationFS(
				"20260804119999_legacy.sql",
				"90000000000000_sequential_versioning.sql",
			), want: "legacy migration maximum = 20260804119999",
		},
		{
			name: "missing anchor", fs: migrationFS("20260804120000_legacy.sql"),
			want: "missing sequential anchor",
		},
		{
			name: "noncanonical filename", fs: migrationFS(
				"20260804120000_legacy.sql",
				"90000000000000_sequential_versioning.sql",
				"01_bad.sql",
			), want: "not a canonical Goose migration filename",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMigrationVersioning(test.fs)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func validateMigrationVersioning(fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	versions := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		match := canonicalMigrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return fmt.Errorf("%q is not a canonical Goose migration filename (expected <positive-integer>_<name>.sql)", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return fmt.Errorf("%q has an invalid migration version: %w", entry.Name(), err)
		}
		if previous, ok := versions[version]; ok {
			return fmt.Errorf("duplicate migration version %d in %q and %q", version, previous, entry.Name())
		}
		versions[version] = entry.Name()
	}

	legacyMax := int64(0)
	for version := range versions {
		if version > legacyMigrationMax && version < sequentialAnchor {
			return fmt.Errorf("migration version %d lies between legacy maximum %d and sequential anchor %d", version, legacyMigrationMax, sequentialAnchor)
		}
		if version < sequentialAnchor && version > legacyMax {
			legacyMax = version
		}
	}
	if legacyMax != legacyMigrationMax {
		return fmt.Errorf("legacy migration maximum = %d, want %d", legacyMax, legacyMigrationMax)
	}
	if _, ok := versions[sequentialAnchor]; !ok {
		return fmt.Errorf("missing sequential anchor migration version %d", sequentialAnchor)
	}

	postAnchor := make([]int64, 0, len(versions))
	for version := range versions {
		if version >= sequentialAnchor {
			postAnchor = append(postAnchor, version)
		}
	}
	slices.Sort(postAnchor)
	for index, version := range postAnchor {
		expected := sequentialAnchor + int64(index)
		if version != expected {
			return fmt.Errorf("sequential migration version %d in %q, expected %d", version, versions[version], expected)
		}
	}
	return nil
}

func migrationFS(names ...string) fs.FS {
	files := make(fstest.MapFS, len(names))
	for _, name := range names {
		files[path.Join("migrations", name)] = &fstest.MapFile{Data: []byte("-- migration")}
	}
	return files
}
