package builddeps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTapSkillVersion(t *testing.T) {
	dir := t.TempDir()
	skill := strings.Join([]string{
		"---",
		"name: tap-web",
		"metadata:",
		"  version: \"v0.4.4\"",
		"---",
		"",
		"# tap-web",
	}, "\n")
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	version, err := tapSkillVersion(path)
	if err != nil {
		t.Fatalf("tapSkillVersion() error = %v", err)
	}
	if version != "v0.4.4" {
		t.Fatalf("version = %q, want v0.4.4", version)
	}
}

func TestSyncTapWebSkillFromBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX sh")
	}
	root := t.TempDir()
	binPath := filepath.Join(root, "tap")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo 'tap version 0.4.4'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"skill\" ] && [ \"$2\" = \"install\" ] && [ \"$3\" = \"--path\" ]; then\n" +
		"  mkdir -p \"$4/references\"\n" +
		"  cat > \"$4/SKILL.md\" <<'EOF'\n---\nname: tap-web\nmetadata:\n  version: \"v0.4.4\"\n---\n# tap-web\nEOF\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "tap-web")
	if err := syncTapWebSkillFromBinary(context.Background(), binPath, dest); err != nil {
		t.Fatalf("syncTapWebSkillFromBinary() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("generated SKILL.md missing: %v", err)
	}
}

func TestSyncTapWebSkillFromBinaryRejectsVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX sh")
	}
	root := t.TempDir()
	binPath := filepath.Join(root, "tap")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo 'tap version 0.4.4'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"skill\" ] && [ \"$2\" = \"install\" ] && [ \"$3\" = \"--path\" ]; then\n" +
		"  mkdir -p \"$4\"\n" +
		"  cat > \"$4/SKILL.md\" <<'EOF'\n---\nname: tap-web\nmetadata:\n  version: \"v0.4.3\"\n---\n# tap-web\nEOF\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := syncTapWebSkillFromBinary(context.Background(), binPath, filepath.Join(root, "tap-web"))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected version mismatch error, got %v", err)
	}
}
