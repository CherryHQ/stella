//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBinaryPathSecurityRejectsUserOwnedBinary(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root-owned temp files do not exercise user-owned rejection")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "stellad")
	if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := checkBinaryPathSecurity(bin)
	if err == nil {
		t.Fatal("expected user-owned binary path to be rejected")
	}
	if !strings.Contains(err.Error(), "root-owned") {
		t.Fatalf("error = %q, want root-owned hint", err.Error())
	}
}

func TestSystemServiceOwnsDockerSandboxMode(t *testing.T) {
	if !strings.Contains(systemUnitTemplate, "Environment=STELLA_DOCKER_SANDBOX_MODE=host") {
		t.Fatal("system service must pin native Docker sandbox mode to host")
	}
}
