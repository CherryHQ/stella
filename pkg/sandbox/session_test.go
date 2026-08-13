package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNopSession(t *testing.T) {
	s := NopSession()
	if s == nil {
		t.Fatal("NopSession returned nil")
	}
	if !s.Alive() {
		t.Error("expected Alive=true before close")
	}
	if s.WorkingDir() != "" {
		t.Errorf("WorkingDir = %q, want empty", s.WorkingDir())
	}

	// Exec should return zero result, no error.
	result, err := s.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Errorf("Exec: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("Exec exit code = %d, want 0", result.ExitCode)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := s.Files().ReadFile(file); err != nil || string(content) != "content" {
		t.Errorf("Files.ReadFile = %q, %v", content, err)
	}

	// Policy should be the default no-op policy.
	policy := s.Policy()
	if policy.Network.Mode != NetworkAllowAll {
		t.Errorf("Policy.Network.Mode = %v, want NetworkAllowAll", policy.Network.Mode)
	}

	// Close should work.
	if err := s.Close(); err != nil {
		t.Errorf("Close: unexpected error: %v", err)
	}
	if s.Alive() {
		t.Error("expected Alive=false after close")
	}
	select {
	case <-s.Done():
	default:
		t.Error("Done channel should be closed after Close")
	}
}
