package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/fsops"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestSnapshotFilesystemCatalogOrdinaryManagedAndPinned(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("managed targets unsupported")
	}
	dir := t.TempDir()
	writeCatalogSkill(t, filepath.Join(dir, "ordinary"), "ordinary", "ordinary", nil)
	digestA := writeManagedCatalogSkill(t, dir, "managed", "old", &skillMetadataEnvelope{Status: SkillStatusActive, Metadata: map[string]any{"v": "old"}, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LegacyLifecycleVersion: 1})
	digestB := writeManagedCatalogSkill(t, dir, "managed", "new", &skillMetadataEnvelope{Status: SkillStatusActive, Metadata: map[string]any{"v": "new"}, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), LegacyLifecycleVersion: 2})
	r, err := fsops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.SwapManagedSkillTarget(context.Background(), "managed", digestA); err != nil {
		t.Fatal(err)
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	snapshot, err := SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if err != nil || len(snapshot.Active) != 2 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", snapshot), "private") {
		t.Fatal("attachment locator leaked into catalog descriptor")
	}
	var managed FilesystemSkillDescriptor
	for _, descriptor := range snapshot.Active {
		if descriptor.Skill.Name == "managed" {
			managed = descriptor
		}
	}
	if managed.Digest != digestA || managed.RevisionPath != "/workspace/.stella-revisions/managed/"+digestA {
		t.Fatalf("managed descriptor = %+v", managed)
	}
	if err := r.SwapManagedSkillTarget(context.Background(), "managed", digestB); err != nil {
		t.Fatal(err)
	}
	data, err := readCatalogFile(context.Background(), filesystem, managed.RevisionPath+"/SKILL.md", maxCatalogSkillBytes)
	if err != nil || string(data) == "" || !contains(string(data), "old") {
		t.Fatalf("pinned read = %q, %v", data, err)
	}
	if contains(managed.RevisionPath, dir) {
		t.Fatalf("host path leaked: %q", managed.RevisionPath)
	}
}

func TestSnapshotFilesystemCatalogRejectsMalformedAndOversized(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	_, err = SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if err == nil {
		t.Fatal("regular file accepted")
	}
	_ = os.Remove(filepath.Join(dir, "file"))
	writeCatalogSkill(t, filepath.Join(dir, "large"), "large", string(make([]byte, maxCatalogSkillBytes+1)), nil)
	_, err = SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if !errors.Is(err, sandbox.ErrReadLimit) {
		t.Fatalf("oversized read = %v", err)
	}
}

func TestSnapshotFilesystemCatalogDeprecatedAndStrictMetadata(t *testing.T) {
	dir := t.TempDir()
	deprecated := &skillMetadataEnvelope{Status: SkillStatusDeprecated, Metadata: map[string]any{}, CreatedAt: time.Time{}, UpdatedAt: time.Time{}, LegacyLifecycleVersion: 1}
	writeCatalogSkill(t, filepath.Join(dir, "old"), "old", "old", deprecated)
	writeCatalogSkill(t, filepath.Join(dir, "bad"), "bad", "bad", nil)
	if err := os.WriteFile(filepath.Join(dir, "bad", skillMetadataFile), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	_, err = SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if err == nil {
		t.Fatal("invalid optional metadata accepted")
	}
	if err := os.RemoveAll(filepath.Join(dir, "bad")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if err != nil || len(snapshot.Active) != 0 || len(snapshot.Deprecated) != 1 {
		t.Fatalf("deprecated snapshot = %+v, %v", snapshot, err)
	}
}

func TestSnapshotFilesystemCatalogFrontmatterFallbackAndScope(t *testing.T) {
	dir := t.TempDir()
	writeCatalogSkill(t, filepath.Join(dir, "fallback"), "", "body", nil)
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	snapshot, err := SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t))
	if err != nil || len(snapshot.Active) != 1 || snapshot.Active[0].Skill.Name != "fallback" {
		t.Fatalf("fallback = %+v, %v", snapshot, err)
	}
	for _, makeRoot := range []func() (FilesystemCatalogRoot, error){
		func() (FilesystemCatalogRoot, error) {
			return UserFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "h", StoreID: "s"}, "u")
		},
		func() (FilesystemCatalogRoot, error) {
			return SystemAgentFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "h", StoreID: "s", Locator: "private"}, "")
		},
		func() (FilesystemCatalogRoot, error) {
			return UserFilesystemCatalogRoot("workspace", sandbox.HomeAttachment{HomeID: "h", StoreID: "s", Locator: "private"}, "u")
		},
	} {
		if _, err := makeRoot(); err == nil {
			t.Fatal("invalid catalog root accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "fallback", "SKILL.md"), []byte("---\nname: forged\ndescription: test\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t)); err == nil {
		t.Fatal("frontmatter mismatch accepted")
	}
}

func TestSnapshotFilesystemCatalogRejectsTamperedManagedRevision(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("managed targets unsupported")
	}
	dir := t.TempDir()
	metadata := &skillMetadataEnvelope{Status: SkillStatusActive, Metadata: map[string]any{}, LegacyLifecycleVersion: 1}
	digest := writeManagedCatalogSkill(t, dir, "managed", "old", metadata)
	r, err := fsops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.SwapManagedSkillTarget(context.Background(), "managed", digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".stella-revisions", "managed", digest, "extra.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	if _, err := SnapshotFilesystemCatalog(context.Background(), filesystem, userCatalogRoot(t)); err == nil {
		t.Fatal("tampered managed revision accepted")
	}
}

func TestCollectManagedTreeCountsEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= maxManagedTreeEntries; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	filesystem, err := fsops.NewFilesystem([]fsops.Mount{{Path: sandbox.PathWorkspace, Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	seen := 0
	if _, err := collectManagedTree(context.Background(), filesystem, sandbox.PathWorkspace, "", 0, &seen, nil); err == nil {
		t.Fatal("empty directories bypassed managed tree entry budget")
	}
	if seen != maxManagedTreeEntries {
		t.Fatalf("entries seen = %d, want %d", seen, maxManagedTreeEntries)
	}
}

func writeCatalogSkill(t *testing.T, directory, name, body string, metadata *skillMetadataEnvelope) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: test\n---\n" + body
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if metadata != nil {
		data, err := encodeSkillMetadataEnvelope(*metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, skillMetadataFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeManagedCatalogSkill(t *testing.T, root, name, body string, metadata *skillMetadataEnvelope) string {
	t.Helper()
	content := []byte("---\nname: " + name + "\ndescription: test\n---\n" + body)
	digest, err := digestSkillTree(skillTree{Metadata: *metadata, Files: []skillTreeEntry{{Path: "SKILL.md", Content: content, Mode: 0o644}}})
	if err != nil {
		t.Fatal(err)
	}
	writeCatalogSkill(t, filepath.Join(root, ".stella-revisions", name, digest), name, body, metadata)
	return digest
}
func contains(s, sub string) bool { return strings.Contains(s, sub) }

func userCatalogRoot(t *testing.T) FilesystemCatalogRoot {
	t.Helper()
	root, err := UserFilesystemCatalogRoot(sandbox.PathWorkspace, sandbox.HomeAttachment{HomeID: "home", StoreID: "store", Locator: "private"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
