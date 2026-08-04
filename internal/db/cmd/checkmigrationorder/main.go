// checkmigrationorder prevents unmerged Goose migrations from sorting before
// migrations already present at the branch base.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const migrationsDir = "internal/db/migrations"

var migrationName = regexp.MustCompile(`^([1-9][0-9]*)_.+\.sql$`)

type migration struct {
	path    string
	version int64
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "migration order check failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("checkmigrationorder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseFlag := flags.String("base", "", "base Git ref or commit SHA")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	baseRef := *baseFlag
	if baseRef == "" {
		baseRef = getenv("STELLA_MIGRATION_BASE_REF")
	}
	if baseRef == "" {
		baseRef = "origin/main"
	}

	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("find repository root: %w", err)
	}
	root = strings.TrimSpace(root)

	return check(root, baseRef, stdout)
}

func check(root, baseRef string, stdout io.Writer) error {
	base, err := gitAt(root, "rev-parse", "--verify", "--quiet", "--end-of-options", baseRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve base %q: %w (fetch it or pass --base <ref-or-sha>)", baseRef, err)
	}
	base = strings.TrimSpace(base)

	baseFiles, err := gitAt(root, "ls-tree", "-r", "-z", "--name-only", base, "--", migrationsDir+"/")
	if err != nil {
		return fmt.Errorf("list migrations at base %s: %w", base, err)
	}
	baseMigrations, err := parseMigrations(splitNUL(baseFiles))
	if err != nil {
		return fmt.Errorf("parse migrations at base %s: %w", base, err)
	}
	baseMax, err := maxVersion(baseMigrations)
	if err != nil {
		return fmt.Errorf("validate migrations at base %s: %w", base, err)
	}

	addedFiles, err := gitAt(root, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=A", base+"...HEAD", "--", ":(glob)"+migrationsDir+"/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations added since base %s: %w", base, err)
	}
	stagedFiles, err := gitAt(root, "diff", "--cached", "--name-only", "-z", "--no-renames", "--diff-filter=A", "--", ":(glob)"+migrationsDir+"/*.sql")
	if err != nil {
		return fmt.Errorf("list staged migrations: %w", err)
	}
	added, err := parseMigrations(uniqueFiles(splitNUL(addedFiles), splitNUL(stagedFiles)))
	if err != nil {
		return fmt.Errorf("parse added migrations: %w", err)
	}
	if len(added) == 0 {
		_, err := fmt.Fprintln(stdout, "Migration order check passed: no migrations added.")
		return err
	}
	if err := validateAdded(baseMax, added); err != nil {
		return fmt.Errorf("base %s (max version %d): %w", base, baseMax, err)
	}

	_, err = fmt.Fprintf(stdout, "Migration order check passed: %d added migration(s) sort after base %s.\n", len(added), base)
	return err
}

func git(args ...string) (string, error) {
	return gitAt("", args...)
}

func gitAt(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	if dir != "" {
		command.Dir = dir
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return "", err
		}
		return "", errors.New(message)
	}
	return string(output), nil
}

func splitNUL(output string) []string {
	return strings.FieldsFunc(output, func(r rune) bool { return r == 0 })
}

func uniqueFiles(fileSets ...[]string) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, fileSet := range fileSets {
		for _, file := range fileSet {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	return files
}

func parseMigrations(files []string) ([]migration, error) {
	migrations := make([]migration, 0, len(files))
	for _, file := range files {
		name := path.Base(file)
		match := migrationName.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("%q is not a canonical Goose migration filename (expected <positive-integer>_<name>.sql)", file)
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q has an invalid migration version: %w", file, err)
		}
		migrations = append(migrations, migration{path: file, version: version})
	}
	return migrations, nil
}

func maxVersion(migrations []migration) (int64, error) {
	var max int64
	versions := make(map[int64]string, len(migrations))
	for _, migration := range migrations {
		if previous, ok := versions[migration.version]; ok {
			return 0, fmt.Errorf("duplicate version %d in %q and %q", migration.version, previous, migration.path)
		}
		versions[migration.version] = migration.path
		if migration.version > max {
			max = migration.version
		}
	}
	return max, nil
}

func validateAdded(baseMax int64, added []migration) error {
	sort.Slice(added, func(i, j int) bool { return added[i].version < added[j].version })
	var previous *migration
	for i := range added {
		migration := &added[i]
		if previous != nil && migration.version <= previous.version {
			return fmt.Errorf("added migrations are not strictly increasing: version %d in %q follows version %d in %q", migration.version, migration.path, previous.version, previous.path)
		}
		if migration.version <= baseMax {
			return fmt.Errorf("%q uses version %d, which must be greater than base maximum %d; rebase on the base branch and renumber this unmerged migration", migration.path, migration.version, baseMax)
		}
		previous = migration
	}
	return nil
}
