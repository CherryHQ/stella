package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDotEnv points STELLA_HOME at a temp dir holding the given .env contents.
func writeDotEnv(t *testing.T, contents string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("STELLA_HOME", home)
}

// TestLoadDotEnvOSNonEmptyWins verifies a variable already present in the OS
// environment is not overwritten by the .env file.
func TestLoadDotEnvOSNonEmptyWins(t *testing.T) {
	writeDotEnv(t, "STELLA_DOTENV_TEST=from_file\n")
	t.Setenv("STELLA_DOTENV_TEST", "from_os")

	LoadDotEnv()

	if got := os.Getenv("STELLA_DOTENV_TEST"); got != "from_os" {
		t.Errorf("value = %q, want from_os (OS wins)", got)
	}
}

// TestLoadDotEnvOSEmptyWins is the precedence fix: an OS variable explicitly set
// to empty is still "present" and must win over the .env file, matching the
// documented "Existing OS/service-injected variables win".
func TestLoadDotEnvOSEmptyWins(t *testing.T) {
	writeDotEnv(t, "STELLA_DOTENV_TEST=from_file\n")
	t.Setenv("STELLA_DOTENV_TEST", "")

	LoadDotEnv()

	if got, ok := os.LookupEnv("STELLA_DOTENV_TEST"); !ok || got != "" {
		t.Errorf("value = %q (present=%v), want empty (explicit-empty OS var wins)", got, ok)
	}
}

// TestLoadDotEnvFillsUnset verifies the .env file supplies a variable absent
// from the OS environment.
func TestLoadDotEnvFillsUnset(t *testing.T) {
	writeDotEnv(t, "STELLA_DOTENV_TEST=from_file\n")
	_ = os.Unsetenv("STELLA_DOTENV_TEST")
	t.Cleanup(func() { _ = os.Unsetenv("STELLA_DOTENV_TEST") })

	LoadDotEnv()

	if got := os.Getenv("STELLA_DOTENV_TEST"); got != "from_file" {
		t.Errorf("value = %q, want from_file (file fills unset)", got)
	}
}
