package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/skills"
)

func TestStorageMigrateSkillsRequiresOfflineProofAndKeepsStdoutClean(t *testing.T) {
	var calls int
	command := migrateSkillsCommand(func(_ context.Context, dsn string, dryRun bool) (skills.SkillMigrationSummary, error) {
		calls++
		if dsn != "postgres://test" || !dryRun {
			t.Fatalf("args = %q, %t", dsn, dryRun)
		}
		return skills.SkillMigrationSummary{DryRun: true, Status: "planned", MarkerState: "pending", SourceCount: 2, Bytes: 7, SHA256: "abc"}, nil
	})
	for _, tt := range []struct {
		args []string
		want string
		fail bool
	}{
		{args: []string{"storage", "migrate-skills", "--confirm-writers-stopped", "--dry-run"}, want: "--confirm-backup-created is required", fail: true},
		{args: []string{"storage", "migrate-skills", "--confirm-backup-created", "--dry-run"}, want: "--confirm-writers-stopped is required", fail: true},
		{args: []string{"storage", "migrate-skills", "--confirm-writers-stopped", "--confirm-backup-created", "--dry-run"}, want: "--confirm-maintenance-mode is required", fail: true},
		{args: []string{"storage", "migrate-skills", "--confirm-writers-stopped", "--confirm-backup-created", "--confirm-maintenance-mode=false", "--dry-run"}, want: "--confirm-maintenance-mode is required", fail: true},
		{args: []string{"storage", "migrate-skills", "--confirm-writers-stopped", "--confirm-backup-created", "--confirm-maintenance-mode", "--dry-run", "--database-url", "postgres://test", "--json"}, want: `{"dry_run":true,"status":"planned","marker_state":"pending","source_count":2,"files":0,"bytes":7,"sha256":"abc","active_count":0,"archive_count":0,"usage_count":0,"unsupported_count":0,"conflict_count":0}` + "\n"},
	} {
		var out, errOut bytes.Buffer
		app := &ucli.App{Writer: &out, ErrWriter: &errOut, Commands: []*ucli.Command{{Name: "storage", Subcommands: []*ucli.Command{command}}}}
		err := app.Run(append([]string{"stellad"}, tt.args...))
		if tt.fail {
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("output=%q stderr=%q", out.String(), errOut.String())
			}
			continue
		}
		if err != nil || out.String() != tt.want || errOut.String() != "Verifying legacy PostgreSQL Skills...\n" {
			t.Fatalf("out=%q stderr=%q err=%v", out.String(), errOut.String(), err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestStorageMigrateSkillsWritesBlockedReportBeforeReturningError(t *testing.T) {
	blocked := skills.SkillMigrationSummary{Status: "blocked", MarkerState: "pending", SourceCount: 1, Issues: []skills.SkillMigrationIssue{{SkillID: "bad", Kind: "unsupported", Reason: "missing SKILL.md"}}}
	command := migrateSkillsCommand(func(context.Context, string, bool) (skills.SkillMigrationSummary, error) {
		return blocked, &skills.SkillMigrationBlockedError{Summary: blocked}
	})
	for _, tt := range []struct {
		json bool
		want string
	}{
		{true, `{"dry_run":false,"status":"blocked","marker_state":"pending","source_count":1,"files":0,"bytes":0,"sha256":"","active_count":0,"archive_count":0,"usage_count":0,"unsupported_count":0,"conflict_count":0,"issues":[{"skill_id":"bad","kind":"unsupported","reason":"missing SKILL.md"}]}` + "\n"},
		{false, "blocked\tpending\t1\t0\t\nbad\tunsupported\tmissing SKILL.md\n"},
	} {
		var out bytes.Buffer
		app := &ucli.App{Writer: &out, Commands: []*ucli.Command{{Name: "storage", Subcommands: []*ucli.Command{command}}}}
		args := []string{"stellad", "storage", "migrate-skills", "--confirm-writers-stopped", "--confirm-backup-created", "--confirm-maintenance-mode"}
		if tt.json {
			args = append(args, "--json")
		}
		err := app.Run(args)
		if err == nil || !strings.Contains(err.Error(), "preflight blocked") || out.String() != tt.want {
			t.Fatalf("json=%t out=%q err=%v", tt.json, out.String(), err)
		}
	}
}

func TestStorageRetryPurgeCommandShapeAndOutput(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	var gotID, gotDSN string
	command := retryPurgeCommand(func(_ context.Context, id, dsn string) (home.Record, error) {
		gotID, gotDSN = id, dsn
		return home.Record{ID: id, State: home.StatePurged}, nil
	})
	var out bytes.Buffer
	app := &ucli.App{Writer: &out, Commands: []*ucli.Command{{Name: "storage", Subcommands: []*ucli.Command{command}}}}
	if err := app.Run([]string{"stellad", "storage", "retry-purge", "--database-url", "postgres://test", "home-1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotID != "home-1" || gotDSN != "postgres://test" {
		t.Fatalf("retry args = %q, %q", gotID, gotDSN)
	}
	if got := out.String(); got != "home-1\tpurged\n" {
		t.Fatalf("human output = %q", got)
	}
}

func TestStorageRetryPurgeCommandJSONAndErrors(t *testing.T) {
	t.Setenv("STELLA_HOME", t.TempDir())
	command := retryPurgeCommand(func(_ context.Context, id, _ string) (home.Record, error) {
		if id == "bad" {
			return home.Record{}, errors.New("not eligible")
		}
		return home.Record{ID: id, State: home.StatePurged}, nil
	})
	for _, tt := range []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{name: "json", args: []string{"storage", "retry-purge", "--json", "home-1"}, want: "{\"home_id\":\"home-1\",\"state\":\"purged\"}"},
		{name: "required id", args: []string{"storage", "retry-purge"}, want: "home ID is required", fail: true},
		{name: "service error", args: []string{"storage", "retry-purge", "bad"}, want: "storage retry-purge: not eligible", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			app := &ucli.App{Writer: &out, Commands: []*ucli.Command{{Name: "storage", Subcommands: []*ucli.Command{command}}}}
			err := app.Run(append([]string{"stellad"}, tt.args...))
			if tt.fail {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output=%q err=%v", out.String(), err)
			}
		})
	}
}

func TestStorageMigrateAssetsCommandOutputProgressAndConfirmation(t *testing.T) {
	var calls int
	command := migrateAssetsCommand(func(_ context.Context, dsn string, dryRun bool) (home.MutableAssetMigrationSummary, error) {
		calls++
		if dsn == "error" {
			return home.MutableAssetMigrationSummary{}, errors.New("injected migration failure")
		}
		if dsn != "postgres://test" {
			t.Fatalf("dsn = %q", dsn)
		}
		return home.MutableAssetMigrationSummary{DryRun: true, Status: "planned", MarkerState: "pending", Count: 2, Bytes: 7, SHA256: "abc"}, nil
	})
	for _, tt := range []struct {
		name   string
		args   []string
		stdout string
		stderr string
		fail   bool
	}{
		{name: "confirmation missing", args: []string{"storage", "migrate-assets", "--dry-run"}, stdout: "", stderr: "", fail: true},
		{name: "confirmation false", args: []string{"storage", "migrate-assets", "--confirm-writers-stopped=false", "--dry-run"}, stdout: "", stderr: "", fail: true},
		{name: "json dry run", args: []string{"storage", "migrate-assets", "--confirm-writers-stopped", "--dry-run", "--database-url", "postgres://test", "--json"}, stdout: `{"dry_run":true,"status":"planned","marker_state":"pending","count":2,"bytes":7,"sha256":"abc"}` + "\n", stderr: "Verifying legacy mutable assets...\n"},
		{name: "human real run", args: []string{"storage", "migrate-assets", "--confirm-writers-stopped", "--database-url", "postgres://test"}, stdout: "planned\tpending\t2\t7\tabc\n", stderr: "Migrating legacy mutable assets...\n"},
		{name: "migration error", args: []string{"storage", "migrate-assets", "--confirm-writers-stopped", "--dry-run", "--database-url", "error"}, stdout: "", stderr: "Verifying legacy mutable assets...\n", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &ucli.App{Writer: &out, ErrWriter: &errOut, Commands: []*ucli.Command{{Name: "storage", Subcommands: []*ucli.Command{command}}}}
			err := app.Run(append([]string{"stellad"}, tt.args...))
			if tt.fail {
				if err == nil {
					t.Fatalf("error = %v", err)
				}
				if strings.Contains(strings.Join(tt.args, " "), "confirm-writers-stopped") && strings.Contains(strings.Join(tt.args, " "), "error") {
					if !strings.Contains(err.Error(), "injected migration failure") {
						t.Fatalf("error = %v", err)
					}
				} else if !strings.Contains(err.Error(), "--confirm-writers-stopped is required") {
					t.Fatalf("error = %v", err)
				}
				if out.String() != tt.stdout || errOut.String() != tt.stderr {
					t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
				}
				return
			}
			if err != nil || out.String() != tt.stdout || errOut.String() != tt.stderr {
				t.Fatalf("stdout=%q stderr=%q err=%v", out.String(), errOut.String(), err)
			}
		})
	}
	if calls != 3 {
		t.Fatalf("migration calls = %d, want 3", calls)
	}
}

func TestRetryFailedPurgeUsesConfiguredLocalStore(t *testing.T) {
	ctx := context.Background()
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	db := dbtest.New(t)
	store, err := home.NewLocalStore("local", stellaHome)
	if err != nil {
		t.Fatal(err)
	}
	failing := &commandFailingPurgeStore{Store: store, fail: true}
	registry, err := home.NewRegistry(db, store.ID(), failing)
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Ensure(ctx, home.Principal(home.UserPrincipal, "retry-command"))
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(stellaHome, filepath.FromSlash(record.Locator), "payload")
	if err := os.WriteFile(payload, []byte("purge me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Tombstone(ctx, record.Key, "delete-actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Purge(ctx, record.ID, "purge-actor"); err == nil {
		t.Fatal("initial purge unexpectedly succeeded")
	}
	failing.fail = false
	purged, err := retryFailedPurge(ctx, record.ID, db.Config().ConnString())
	if err != nil || purged.State != home.StatePurged {
		t.Fatalf("retryFailedPurge = %#v, %v", purged, err)
	}
	if _, err := os.Stat(payload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged bytes stat = %v, want absent", err)
	}
}

type commandFailingPurgeStore struct {
	home.Store
	fail bool
}

func (s *commandFailingPurgeStore) Purge(ctx context.Context, record home.Record) error {
	if s.fail {
		return errors.New("injected physical delete failure")
	}
	return s.Store.Purge(ctx, record)
}
