package sessionfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestAccessPinsMountSourceAcrossPathReplacement(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "value"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver("/workspace", []Mount{{HostPath: source, SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	access := NewAccess(resolver)

	moved := filepath.Join(parent, "moved")
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "value"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, err := access.ReadFile("value")
	if err != nil || string(content) != "original" {
		t.Fatalf("pinned read = %q, %v", content, err)
	}
	if err := access.WriteFile("written", []byte("pinned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(moved, "written")); err != nil || string(content) != "pinned" {
		t.Fatalf("pinned write = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(source, "written")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement source received pinned write: %v", err)
	}
	if err := resolver.ValidateBackingPaths(); err == nil {
		t.Fatal("replaced mount source passed backing-path validation")
	}
}

func TestAccessConfinesSymlinksAndEnforcesReadOnlyMounts(t *testing.T) {
	writable := t.TempDir()
	readOnly := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(writable, "escape")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver("/workspace", []Mount{
		{HostPath: writable, SandboxPath: "/workspace"},
		{HostPath: readOnly, SandboxPath: "/readonly", ReadOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	access := NewAccess(resolver)

	if _, err := access.ReadFile("escape"); err == nil {
		t.Fatal("out-of-root symlink read succeeded")
	}
	if _, err := resolver.ResolveDirectory("escape"); err == nil {
		t.Fatal("out-of-root symlink cwd succeeded")
	}
	if err := access.WriteFile("/readonly/file", []byte("write"), 0o600); err == nil {
		t.Fatal("read-only mount write succeeded")
	}
}

func TestNewResolverRequiresDisjointMountPartitions(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	sibling := t.TempDir()
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mounts []Mount
	}{
		{
			name: "canonical parent child",
			mounts: []Mount{
				{HostPath: parent, SandboxPath: "/workspace"},
				{HostPath: sibling, SandboxPath: "/workspace/nested"},
			},
		},
		{
			name: "canonical child parent reverse order",
			mounts: []Mount{
				{HostPath: sibling, SandboxPath: "/workspace/nested"},
				{HostPath: parent, SandboxPath: "/workspace"},
			},
		},
		{
			name: "physical parent child",
			mounts: []Mount{
				{HostPath: parent, SandboxPath: "/workspace"},
				{HostPath: child, SandboxPath: "/user"},
			},
		},
		{
			name: "physical child parent reverse order",
			mounts: []Mount{
				{HostPath: child, SandboxPath: "/user"},
				{HostPath: parent, SandboxPath: "/workspace"},
			},
		},
		{
			name: "same physical root",
			mounts: []Mount{
				{HostPath: parent, SandboxPath: "/workspace"},
				{HostPath: parent, SandboxPath: "/user"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver, err := NewResolver("/workspace", test.mounts)
			if err == nil {
				_ = resolver.Close()
				t.Fatal("overlapping mount plan was accepted")
			}
		})
	}

	resolver, err := NewResolver("/work", []Mount{
		{HostPath: parent, SandboxPath: "/work"},
		{HostPath: sibling, SandboxPath: "/workspace"},
	})
	if err != nil {
		t.Fatalf("segment-lookalike siblings were rejected: %v", err)
	}
	if err := resolver.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewResolverRejectsPhysicalOverlapThroughSymlinkedSource(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver("/workspace", []Mount{
		{HostPath: parent, SandboxPath: "/workspace"},
		{HostPath: alias, SandboxPath: "/user"},
	})
	if err == nil {
		_ = resolver.Close()
		t.Fatal("symlink-hidden physical overlap was accepted")
	}
}

func TestAccessRejectsSymlinkAcrossMountPartitions(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	user := filepath.Join(parent, "user")
	for _, directory := range []string{workspace, user} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(user, "value"), []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../user/value", filepath.Join(workspace, "cross")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver("/workspace", []Mount{
		{HostPath: workspace, SandboxPath: "/workspace"},
		{HostPath: user, SandboxPath: "/user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	if _, err := NewAccess(resolver).ReadFile("cross"); err == nil {
		t.Fatal("cross-partition symlink was followed")
	}
}

func TestPolicyMountsExposeOnlyProcessViewAndAccess(t *testing.T) {
	physical := t.TempDir()
	got := PolicyMounts([]Mount{
		{HostPath: physical, SandboxPath: "/workspace"},
		{HostPath: filepath.Join(physical, "not-exposed"), SandboxPath: "/readonly", ReadOnly: true},
	})
	if len(got) != 2 {
		t.Fatalf("PolicyMounts length = %d, want 2", len(got))
	}
	if got[0].SandboxPath != "/workspace" || got[0].Access != pkgsandbox.MountReadWrite {
		t.Fatalf("writable policy mount = %+v", got[0])
	}
	if got[1].SandboxPath != "/readonly" || got[1].Access != pkgsandbox.MountReadOnly {
		t.Fatalf("read-only policy mount = %+v", got[1])
	}
}

func TestProjectFilesPublishesOneExactTreeAndRejectsPoisoning(t *testing.T) {
	root := t.TempDir()
	resolver, err := NewResolver("/tmp", []Mount{{HostPath: root, SandboxPath: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	files := []pkgsandbox.ProjectedFile{
		{Path: "SKILL.md", Content: []byte("# exact\n"), Mode: 0o444},
		{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\nprintf exact"), Mode: 0o555},
	}

	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			errs <- NewAccess(resolver).ProjectFiles("stella-skills/digest", files)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent projection: %v", err)
		}
	}
	target := filepath.Join(root, "stella-skills", "digest")
	if content, err := os.ReadFile(filepath.Join(target, "scripts", "run.sh")); err != nil || string(content) != "#!/bin/sh\nprintf exact" {
		t.Fatalf("projected file = %q, %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stella-project-") {
			t.Fatalf("unpublished stage remains: %q", entry.Name())
		}
	}
	poisoned := filepath.Join(target, "SKILL.md")
	if err := os.Chmod(poisoned, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poisoned, []byte("poisoned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewAccess(resolver).ProjectFiles("stella-skills/digest", files); !errors.Is(err, pkgsandbox.ErrProjectionConflict) {
		t.Fatalf("poisoned projection = %v, want ErrProjectionConflict", err)
	}
	if content, err := os.ReadFile(poisoned); err != nil || string(content) != "poisoned" {
		t.Fatalf("poisoned target was replaced: %q, %v", content, err)
	}
}

func TestProjectFilesRejectsNestedProcessMountPlan(t *testing.T) {
	workspace := t.TempDir()
	nested := t.TempDir()
	resolver, err := NewResolver("/workspace", []Mount{
		{HostPath: workspace, SandboxPath: "/workspace"},
		{HostPath: nested, SandboxPath: "/workspace/projection/digest/scripts"},
	})
	if err == nil {
		_ = resolver.Close()
		t.Fatal("nested process mount plan was accepted")
	}
	if _, err := os.Stat(filepath.Join(workspace, "projection", "digest")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-mount projection wrote target: %v", err)
	}
}
