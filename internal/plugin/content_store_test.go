package plugin

import (
	"bytes"
	"context"
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

func TestContentStoreCleanupEnumeratesOrphansAndResumesMissingRoots(t *testing.T) {
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keep := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	retired := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	orphan := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	for _, digest := range []string{keep, retired, orphan} {
		if err := os.Mkdir(filepath.Join(store.root, digest), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var got map[string]bool
	err = store.cleanup(context.Background(), func(context.Context) (contentCleanupSelection, error) {
		return contentCleanupSelection{keep: []string{keep}, candidates: []string{keep, retired}}, nil
	}, func(_ context.Context, removed map[string]bool) error {
		got = removed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got[retired] || !got[orphan] || got[keep] {
		t.Fatalf("removed roots = %#v, want retired and orphan only", got)
	}
	for _, digest := range []string{retired, orphan} {
		if _, err := os.Stat(filepath.Join(store.root, digest)); !os.IsNotExist(err) {
			t.Fatalf("digest %s still exists: %v", digest, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.root, keep)); err != nil {
		t.Fatalf("kept digest missing: %v", err)
	}

	// A failed DB finalizer after byte removal leaves no live or quarantine
	// directory. The next pass must still report the retired root as removed,
	// including after a fresh ContentStore is constructed for restart recovery.
	store, err = NewContentStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	var retried bool
	err = store.cleanup(context.Background(), func(context.Context) (contentCleanupSelection, error) {
		return contentCleanupSelection{candidates: []string{retired}}, nil
	}, func(_ context.Context, removed map[string]bool) error {
		retried = removed[retired]
		return nil
	})
	if err != nil || !retried {
		t.Fatalf("restart cleanup removed=%v err=%v, want absence success", retried, err)
	}
}

func TestContentStoreCleanupRemovesStaleQuarantineKeepsRepublishedLiveRoot(t *testing.T) {
	store, err := NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err := os.Mkdir(filepath.Join(store.root, digest), 0o700); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(store.root, ".quarantine-"+digest+"-stale")
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	err = store.cleanup(context.Background(), func(context.Context) (contentCleanupSelection, error) {
		return contentCleanupSelection{keep: []string{digest}}, nil
	}, func(_ context.Context, removed map[string]bool) error {
		if !removed[digest] {
			t.Fatal("stale quarantine was not removed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.root, digest)); err != nil {
		t.Fatalf("republished live digest was removed: %v", err)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("stale quarantine remains: %v", err)
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
