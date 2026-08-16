package main

import (
	"os"
	"path/filepath"
	"testing"

	ucli "github.com/urfave/cli/v2"
)

func runInstallQualification(t *testing.T, root, record string) error {
	t.Helper()
	app := &ucli.App{Commands: []*ucli.Command{storageCommand()}}
	return app.RunContext(t.Context(), []string{"stellad", "storage", "install-qualification", "--root", root, "--record", record})
}

func TestInstallQualificationRejectsMinimalRecordAndAcceptsHarnessOutput(t *testing.T) {
	minimal := filepath.Join(t.TempDir(), "minimal.json")
	if err := os.WriteFile(minimal, []byte(`{"namespace_identity":"forged","qualified_shared":true,"overall_pass":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInstallQualification(t, t.TempDir(), minimal); err == nil {
		t.Fatal("minimal qualification record installed")
	}
	reference, err := filepath.Abs("../../docs/qualification/shared-posix/juicefs-ce-1.4.1-orb-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := runInstallQualification(t, root, reference); err != nil {
		t.Fatalf("actual harness record rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stella-shared-posix", "qualification.json")); err != nil {
		t.Fatal(err)
	}
}
