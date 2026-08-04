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
)

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
