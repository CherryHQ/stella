//go:build system

package system

import (
	"os/exec"
	"strings"
	"testing"

	"filippo.io/age"
)

// TestCandidateOperatorCommands proves the release candidate, rather than an
// in-process command tree, exposes every local operator command. A deliberately
// invalid server-only setting also proves these commands do not parse server
// configuration before dispatching.
func TestCandidateOperatorCommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		markers []string
	}{
		{
			name:    "root",
			args:    []string{"--help"},
			markers: []string{"server", "version", "upgrade", "postgres", "vault", "mise", "service"},
		},
		{
			name: "version",
			args: []string{"version"},
		},
		{
			name:    "upgrade",
			args:    []string{"upgrade", "--help"},
			markers: []string{"GitHub release"},
		},
		{
			name:    "postgres",
			args:    []string{"postgres", "download-runtime", "--help"},
			markers: []string{"PostgreSQL runtime"},
		},
		{
			name:    "vault",
			args:    []string{"vault", "keygen", "--help"},
			markers: []string{"STELLA_VAULT_KEY"},
		},
		{
			name:    "mise",
			args:    []string{"mise", "reconcile-builtins", "--help"},
			markers: []string{"builtin manifest tools"},
		},
		{
			name:    "service",
			args:    []string{"service", "--help"},
			markers: []string{"install", "uninstall", "restart", "status", "logs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath(t), tt.args...)
			cmd.Env = append(baseSubprocessEnv(),
				"STELLA_HOME="+t.TempDir(),
				"AUTH_SESSION_TTL=not-a-duration",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("candidate %v failed: %v\n%s", tt.args, err, out)
			}
			text := string(out)
			if strings.TrimSpace(text) == "" {
				t.Fatalf("candidate %v returned empty output", tt.args)
			}
			for _, marker := range tt.markers {
				if !strings.Contains(text, marker) {
					t.Errorf("candidate %v output missing %q:\n%s", tt.args, marker, text)
				}
			}
		})
	}

	t.Run("vault keygen returns a usable identity", func(t *testing.T) {
		cmd := exec.Command(binaryPath(t), "vault", "keygen")
		cmd.Env = append(baseSubprocessEnv(),
			"STELLA_HOME="+t.TempDir(),
			"AUTH_SESSION_TTL=not-a-duration",
		)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("candidate vault keygen failed: %v", err)
		}
		// Parse the generated identity without logging the private key.
		if _, err := age.ParseX25519Identity(strings.TrimSpace(string(out))); err != nil {
			t.Fatalf("candidate vault keygen returned an invalid identity: %v", err)
		}
	})

	t.Run("unknown command fails closed", func(t *testing.T) {
		cmd := exec.Command(binaryPath(t), "definitely-not-a-command")
		cmd.Env = append(baseSubprocessEnv(), "STELLA_HOME="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("unknown command unexpectedly succeeded:\n%s", out)
		}
	})
}
