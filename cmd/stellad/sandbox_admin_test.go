package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func runSandboxCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	app := &ucli.App{Commands: []*ucli.Command{sandboxCommand()}, Writer: &output, ErrWriter: &output}
	err := app.RunContext(t.Context(), append([]string{"stellad", "sandbox"}, args...))
	return output.String(), err
}

func TestSandboxMaintenanceRejectsUnsafeArgumentsBeforeOpeningDatabase(t *testing.T) {
	t.Setenv("STELLA_DATABASE_URL", "invalid-maintenance-database")
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"missing target", []string{"inspect"}, "exactly one session ID"},
		{"ambiguous target", []string{"inspect", "first", "second"}, "exactly one session ID"},
		{"invalid generation", []string{"reconcile", "--generation", "0", "session"}, "positive number"},
		{"invalid owner", []string{"acknowledge-absent", "--generation", "1", "--owner-boot", "not-a-uuid", "--reason", "verified", "--force", "session"}, "executor UUID"},
		{"missing confirmation", []string{"acknowledge-absent", "--generation", "1", "--owner-boot", uuid.NewString(), "--reason", "verified", "session"}, "explicit --force"},
		{"blank audit reason", []string{"acknowledge-absent", "--generation", "1", "--owner-boot", uuid.NewString(), "--reason", "  ", "--force", "session"}, "non-empty --reason"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runSandboxCommand(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q before opening database", err, tt.want)
			}
		})
	}
}

func TestMaintenanceRequiresExistingStoppedEmbeddedDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "postgres")
	if err := requireStoppedMaintenanceDatabase(dataDir); err == nil {
		t.Fatal("missing database was accepted")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("18\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "postmaster.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireStoppedMaintenanceDatabase(dataDir); err == nil {
		t.Fatal("possible active database was accepted")
	}
	if err := os.Remove(filepath.Join(dataDir, "postmaster.pid")); err != nil {
		t.Fatal(err)
	}
	if err := requireStoppedMaintenanceDatabase(dataDir); err != nil {
		t.Fatalf("stopped existing database: %v", err)
	}
}

func TestSandboxMaintenanceUsesDurableOwnerAndRecordsExactAcknowledgement(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID, bootID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO runtime_executor_boot(id, status) VALUES ($1, 'running')`, bootID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_sandbox_generation
        (session_id, generation, owner_boot_id, backend, config_digest, state, fenced_at)
        VALUES ($1, 1, $2, 'none', 'test-policy', 'unknown', clock_timestamp())`, sessionID, bootID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STELLA_DATABASE_URL", db.Config().ConnString())
	output, err := runSandboxCommand(t, "inspect", "--json", sessionID)
	if err != nil {
		t.Fatalf("inspect using maintenance connection: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(output), &row); err != nil {
		t.Fatalf("inspect JSON: %v", err)
	}
	if row["owner_boot_id"] != bootID || row["state"] != "unknown" {
		t.Fatalf("inspect returned wrong durable identity: %s", output)
	}
	resource, ok := row["resource"].(map[string]any)
	if !ok || resource["backend"] != "none" {
		t.Fatalf("resource JSON has no stable backend field: %s", output)
	}
	ack := []string{"acknowledge-absent", "--generation", "1", "--owner-boot", bootID, "--reason", "Verified the old host was powered off", "--force", "--json", sessionID}
	if _, err := runSandboxCommand(t, ack...); err == nil {
		t.Fatal("a live owner was acknowledged as absent")
	}
	if _, err := db.Exec(ctx, `UPDATE runtime_executor_boot SET status='drained', drained_at=clock_timestamp() WHERE id=$1`, bootID); err != nil {
		t.Fatal(err)
	}
	output, err = runSandboxCommand(t, ack...)
	if err != nil {
		t.Fatalf("acknowledge drained exact owner: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &row); err != nil {
		t.Fatal(err)
	}
	if row["state"] != "destroyed" || !strings.Contains(row["last_error"].(string), "Verified the old host was powered off") {
		t.Fatalf("acknowledgement missing state or audit reason: %s", output)
	}
}
