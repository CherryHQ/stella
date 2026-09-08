package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestContentStoreReadPackageSkillReturnsFilesAndModes(t *testing.T) {
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	published, err := agentpackage.PublishDirectory(filepath.Join("agentpackage", "testdata", "plain"), store.root)
	if err != nil {
		t.Fatal(err)
	}
	files, modes, err := store.ReadPackageSkill(t.Context(), strings.TrimPrefix(published.Digest, "sha256:"), "acme-tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || modes["SKILL.md"] == 0 {
		t.Fatalf("files/modes = %#v/%#v", files, modes)
	}
}

func TestContentStorePackageSkillKeepsExactVersionAndRejectsTampering(t *testing.T) {
	source := t.TempDir()
	if err := os.CopyFS(source, os.DirFS(filepath.Join("agentpackage", "testdata", "plain"))); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(source, "skills", "acme-tools")
	script := []byte("#!/bin/sh\nprintf v1")
	binary := []byte{0x00, 0xff, 0x80, 0x01}
	for name, data := range map[string][]byte{"run.sh": script, "data.bin": binary} {
		if err := os.WriteFile(filepath.Join(skillDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(skillDir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v1, err := agentpackage.PublishDirectory(source, store.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "run.sh"), []byte("#!/bin/sh\nprintf v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	v2, err := agentpackage.PublishDirectory(source, store.root)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Digest == v2.Digest {
		t.Fatal("changed script did not change package digest")
	}
	oldFiles, modes, err := store.ReadPackageSkill(t.Context(), strings.TrimPrefix(v1.Digest, "sha256:"), "acme-tools")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldFiles["run.sh"], script) || !bytes.Equal(oldFiles["data.bin"], binary) || modes["run.sh"].Perm() != 0o755 {
		t.Fatalf("old package bytes or executable mode changed: files=%v modes=%v", oldFiles, modes)
	}
	newFiles, _, err := store.ReadPackageSkill(t.Context(), strings.TrimPrefix(v2.Digest, "sha256:"), "acme-tools")
	if err != nil || string(newFiles["run.sh"]) != "#!/bin/sh\nprintf v2" {
		t.Fatalf("new package script = %q, error = %v", newFiles["run.sh"], err)
	}
	// Same-UID processes can alter a local tree. The next controlled read must
	// detect that change instead of serving bytes under the old identity.
	target := filepath.Join(store.root, strings.TrimPrefix(v1.Digest, "sha256:"), "skills", "acme-tools", "run.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadPackageSkill(t.Context(), strings.TrimPrefix(v1.Digest, "sha256:"), "acme-tools"); err == nil {
		t.Fatal("tampered package was returned under its original digest")
	}
	if !bytes.Equal(oldFiles["run.sh"], script) {
		t.Fatal("previously copied bytes changed after store tampering")
	}
}
