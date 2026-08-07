package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

func TestFilesystemTempDirLinuxUsesSandboxPath(t *testing.T) {
	if got := filesystemTempDir([]tmpMount{{sandboxPath: "/tmp", realPath: "/host/principal-tmp"}}); got != "/tmp" {
		t.Errorf("filesystemTempDir = %q, want /tmp", got)
	}
}

// TestWrapCommand_linux_networkDisabled verifies that wrapCommand on Linux
// uses bwrap with --unshare-net when network is disabled, or returns an error
// when bwrap is not functional (no fallback to unshare).
func TestWrapCommand_linux_networkDisabled(t *testing.T) {
	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
	}

	path, args, err := wrapCommand(policy, layoutFor(root, root), sandboxCwd, nil, "", "sh", []string{"-c", "echo hi"})

	if !bwrapFunctional() {
		if err == nil {
			t.Fatal("expected error when bwrap is not functional, got nil")
		}
		if !strings.Contains(err.Error(), "bwrap") {
			t.Errorf("error should mention bwrap, got: %v", err)
		}
		return
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "bwrap") {
		t.Errorf("expected bwrap executable, got %q", path)
	}
	if !slices.Contains(args, "--unshare-net") {
		t.Errorf("expected --unshare-net in bwrap args, got %v", args)
	}
}

// TestWrapCommand_linux_allowAllNoWrap verifies that when network is allow_all,
// bwrap is used without --unshare-net.
func TestWrapCommand_linux_allowAllNoWrap(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	_, args, err := wrapCommand(policy, layoutFor(root, root), sandboxCwd, nil, "", "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error for allow_all network: %v", err)
	}

	for _, a := range args {
		if a == "--unshare-net" {
			t.Errorf("--unshare-net should not appear for allow_all network, got args %v", args)
		}
	}
}

func TestWrapCommandLinuxBindsEachSessionTempMountOnce(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	workspace := t.TempDir()
	tmp, varTmp := t.TempDir(), t.TempDir()
	tmpMounts := []tmpMount{
		{sandboxPath: "/tmp", realPath: tmp},
		{sandboxPath: "/var/tmp", realPath: varTmp},
	}
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: tmp, Target: "/tmp", Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: varTmp, Target: "/var/tmp", Access: hostlayout.ReadWrite},
	)
	_, args, err := wrapCommand(sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}}, layout, sandboxpkg.PathWorkspace, tmpMounts, "", "sh", []string{"-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, temp := range tmpMounts {
		count := 0
		for i := range args {
			if args[i] == "--bind" && i+2 < len(args) && args[i+1] == temp.realPath && args[i+2] == temp.sandboxPath {
				count++
			}
		}
		if count != 1 {
			t.Errorf("bind %q -> %q appears %d times; want once: %v", temp.realPath, temp.sandboxPath, count, args)
		}
	}
}

// TestWrapCommand_linux_bwrapWorkspaceRemap verifies that when bwrap is
// available, the args include --dir /workspace, --bind <realRoot> /workspace,
// and --chdir <sandboxCwd>.
func TestLocalExec_linux_workspaceWritableOutsideHidden(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}

	s := &localSession{
		id: "test",
		policy: sandboxpkg.Policy{
			Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: root},
			Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
			Env: map[string]string{
				"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME": "/workspace",
			},
		},
		layout:      layoutFor(root, root),
		realRoot:    root,
		sandboxRoot: "/workspace",
		done:        make(chan struct{}),
	}

	cmd := "echo ok > /workspace/out.txt && test ! -e " + shellQuote(outsideFile) + " && cat /workspace/out.txt"
	result, err := s.Exec(context.Background(), cmd, sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v (stderr=%q)", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("stdout=%q, want ok", result.Stdout)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestAppendResolvedFileMount_mountsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	link := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	args := appendResolvedFileMount(nil, link)
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--ro-bind\x00"+target+"\x00"+target) {
		t.Fatalf("expected resolved target bind for %q, got %v", target, args)
	}
}

func TestWrapCommand_linux_bwrapWorkspaceRemap(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace/sub"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: sandboxCwd,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	execPath, args, err := wrapCommand(policy, layoutFor(root, root), sandboxCwd, nil, "", "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(execPath, "bwrap") {
		t.Errorf("expected bwrap, got %q", execPath)
	}

	flagIndex := func(flag string) int {
		for i, a := range args {
			if a == flag {
				return i
			}
		}
		return -1
	}
	hasFlag := func(flag, val string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == val {
				return true
			}
		}
		return false
	}
	hasFlagPair := func(flag, first, second string) bool {
		for i, a := range args {
			if a == flag && i+2 < len(args) && args[i+1] == first && args[i+2] == second {
				return true
			}
		}
		return false
	}
	hasSingle := func(flag string) bool {
		return flagIndex(flag) >= 0
	}

	for _, flag := range []string{"--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts"} {
		if !hasSingle(flag) {
			t.Errorf("expected %s in bwrap args", flag)
		}
	}
	if !hasSingle("--dir") {
		t.Error("expected --dir in bwrap args")
	}
	if !hasFlag("--dir", "/workspace") {
		t.Errorf("expected --dir /workspace in bwrap args, got %v", args)
	}
	if !hasFlag("--tmpfs", "/dev/shm") {
		t.Errorf("expected --tmpfs /dev/shm for common Linux IPC/tmp users, got %v", args)
	}
	if !hasFlag("--tmpfs", "/var/tmp") {
		t.Errorf("expected --tmpfs /var/tmp for common Linux temp users, got %v", args)
	}
	if !hasFlagPair("--bind", root, "/workspace") {
		t.Errorf("expected --bind %s /workspace in bwrap args, got %v", root, args)
	}
	if hasFlagPair("--ro-bind", "/", "/") {
		t.Errorf("local bwrap must not expose the whole host root read-only, got %v", args)
	}
	roBindIndex := flagIndex("--ro-bind")
	bindIndex := flagIndex("--bind")
	if roBindIndex < 0 || bindIndex < 0 || roBindIndex > bindIndex {
		t.Errorf("expected read-only runtime mounts before writable --bind %s /workspace, got %v", root, args)
	}
	if !hasFlag("--chdir", sandboxCwd) {
		t.Errorf("expected --chdir %s in bwrap args, got %v", sandboxCwd, args)
	}
}

// TestWrapCommand_linux_outOfRootWritableBind verifies a writable mount that
// lives under STELLA_HOME (outside both top-level roots) is bound writable
// (--bind, not --ro-bind) at its STELLA_HOME-remapped path, while the rest of
// STELLA_HOME stays read-only. (The per-user mise tree no longer takes this path
// on Linux — it lives under /user — but the mechanism still backs macOS and any
// future out-of-root writable.)
func TestWrapCommand_linux_outOfRootWritableBind(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	stellaHome := t.TempDir()
	userDir := filepath.Join(stellaHome, "users", "u1", ".mise-tools")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sandboxUserDir := filepath.Join(sandboxStellaHome, "users", "u1", ".mise-tools")

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	workspace := t.TempDir()
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: userDir, Target: sandboxUserDir, Access: hostlayout.ReadWrite})
	_, args, err := wrapCommand(policy, layout, "/workspace", nil, stellaHome, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasFlagPair := func(flag, first, second string) bool {
		for i, a := range args {
			if a == flag && i+2 < len(args) && args[i+1] == first && args[i+2] == second {
				return true
			}
		}
		return false
	}
	if !hasFlagPair("--bind", userDir, sandboxUserDir) {
		t.Errorf("expected writable --bind %s %s for the per-user mise tree, got %v", userDir, sandboxUserDir, args)
	}
	if hasFlagPair("--ro-bind", userDir, sandboxUserDir) {
		t.Errorf("per-user mise tree must be writable, not --ro-bind, got %v", args)
	}
}

// TestWrapCommand_linux_inWorkspaceWritableMountSkipped verifies a writable mount
// inside the workspace is NOT bound again under the STELLA_HOME tree: it is already
// writable via the realRoot -> /workspace bind, and a second bind would only
// re-expose the host path.
func TestWrapCommand_linux_inWorkspaceWritableMountSkipped(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	stellaHome := t.TempDir()
	userHome := filepath.Join(stellaHome, "users", "u1")
	miseDir := filepath.Join(userHome, ".mise-tools")
	if err := os.MkdirAll(miseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sandboxMiseDir := filepath.Join(sandboxStellaHome, "users", "u1", ".mise-tools")

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	layout := layoutFor(userHome, userHome, hostlayout.Mount{Source: userHome, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite})
	_, args, err := wrapCommand(policy, layout, "/workspace", nil, stellaHome, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(args, " ")
	if strings.Contains(joined, sandboxMiseDir) {
		t.Errorf("in-workspace writable mount must not be re-bound under %s: %v", sandboxStellaHome, args)
	}
	if !strings.Contains(joined, "--bind "+userHome+" /workspace") {
		t.Errorf("expected realRoot bind to /workspace covering the mise tree, got %v", args)
	}
}

// TestWrapCommand_linux_inUserDataWritableMountUsesDeclaredTarget verifies that
// a nested source remains bound at its distinct declared target even when a
// writable /user root also contains it.
func TestWrapCommand_linux_inUserDataWritableMountUsesDeclaredTarget(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	stellaHome := t.TempDir()
	agentDir := filepath.Join(stellaHome, "users", "u1", "agents", "a1")
	userData := filepath.Join(stellaHome, "users", "u1", "data")
	writableDir := filepath.Join(userData, "scratch")
	if err := os.MkdirAll(writableDir, 0o755); err != nil {
		t.Fatal(err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	sandboxWritableDir := filepath.Join(sandboxStellaHome, "users", "u1", "data", "scratch")
	layout := layoutFor(agentDir, agentDir,
		hostlayout.Mount{Source: agentDir, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: userData, Target: sandboxpkg.MountUserData, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: writableDir, Target: sandboxWritableDir, Access: hostlayout.ReadWrite})
	_, args, err := wrapCommand(policy, layout, "/workspace", nil, stellaHome, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasBind := func(source, target string) bool {
		for i := range args {
			if args[i] == "--bind" && i+2 < len(args) && args[i+1] == source && args[i+2] == target {
				return true
			}
		}
		return false
	}
	if !hasBind(userData, "/user") {
		t.Errorf("expected --bind %s /user covering the writable mount, got %v", userData, args)
	}
	if !hasBind(writableDir, sandboxWritableDir) {
		t.Errorf("expected declared --bind %s %s, got %v", writableDir, sandboxWritableDir, args)
	}
}

func TestLocalSessionLinuxExternalReadOnlyMountMatchesFilesystem(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	workspace, locked := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(locked, "secret"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: locked, Target: sandboxpkg.PathWorkspace + "/locked", Access: hostlayout.ReadOnly})
	session, err := NewFactory(Config{Layout: layout}).CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workspace},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck

	filesystem, err := session.(sandboxpkg.FilesystemSession).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close() //nolint:errcheck
	r, _, err := filesystem.Read(context.Background(), "/workspace/locked/secret", sandboxpkg.ReadOptions{MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || string(contents) != "external\n" {
		t.Fatalf("Filesystem nested mount read = %q, %v, %v", contents, readErr, closeErr)
	}
	if err := filesystem.Write(context.Background(), "/workspace/locked/new", strings.NewReader("no"), sandboxpkg.WriteOptions{}); err == nil {
		t.Fatal("Filesystem wrote nested read-only mount")
	}

	result, err := session.Exec(context.Background(), `cat /workspace/locked/secret && ! printf no > /workspace/locked/new`, sandboxpkg.ExecOptions{})
	if err != nil || result.ExitCode != 0 || result.Stdout != "external\n" {
		t.Fatalf("Exec nested mount = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(locked, "new")); !os.IsNotExist(err) {
		t.Fatalf("bwrap wrote read-only external source: %v", err)
	}
}

func TestLocalSessionLinuxReadOnlyWorkspaceWithWritableChildMatchesFilesystem(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	base := t.TempDir()
	project, other := filepath.Join(base, "project"), filepath.Join(base, "other")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := hostlayout.Layout{
		WorkspaceSource: project, WorkingDirSource: project,
		Mounts: []hostlayout.Mount{
			{Source: base, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadOnly},
			{Source: project, Target: sandboxpkg.PathWorkspace + "/project", Access: hostlayout.ReadWrite},
		},
	}
	session, err := NewFactory(Config{Layout: layout}).CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: project},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck

	filesystem, err := session.(sandboxpkg.FilesystemSession).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close() //nolint:errcheck
	if err := filesystem.Write(context.Background(), "/workspace/other/no", strings.NewReader("no"), sandboxpkg.WriteOptions{}); err == nil {
		t.Fatal("Filesystem wrote read-only workspace root")
	}
	if err := filesystem.Write(context.Background(), "/workspace/project/yes", strings.NewReader("yes"), sandboxpkg.WriteOptions{}); err != nil {
		t.Fatalf("Filesystem wrote declared child: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(project, "yes")); err != nil || string(got) != "yes" {
		t.Fatalf("Filesystem child bytes = %q, %v", got, err)
	}

	result, err := session.Exec(context.Background(), `! printf no > /workspace/other/no && printf yes > /workspace/project/exec`, sandboxpkg.ExecOptions{})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Exec workspace root/child = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(other, "no")); !os.IsNotExist(err) {
		t.Fatalf("bwrap wrote read-only workspace root: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(project, "exec")); err != nil || string(got) != "yes" {
		t.Fatalf("bwrap child bytes = %q, %v", got, err)
	}
}

func TestLocalSessionLinuxReadOnlyUserMountMatchesFilesystem(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	workspace, userData := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(userData, "secret"), []byte("user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: userData, Target: sandboxpkg.PathUser, Access: hostlayout.ReadOnly})
	session, err := NewFactory(Config{Layout: layout}).CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workspace},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck

	filesystem, err := session.(sandboxpkg.FilesystemSession).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close() //nolint:errcheck
	r, _, err := filesystem.Read(context.Background(), "/user/secret", sandboxpkg.ReadOptions{MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || string(contents) != "user\n" {
		t.Fatalf("Filesystem read-only user read = %q, %v, %v", contents, readErr, closeErr)
	}
	if err := filesystem.Write(context.Background(), "/user/new", strings.NewReader("no"), sandboxpkg.WriteOptions{}); err == nil {
		t.Fatal("Filesystem wrote read-only /user")
	}

	result, err := session.Exec(context.Background(), `cat /user/secret && ! printf no > /user/new`, sandboxpkg.ExecOptions{})
	if err != nil || result.ExitCode != 0 || result.Stdout != "user\n" {
		t.Fatalf("Exec read-only /user = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(userData, "new")); !os.IsNotExist(err) {
		t.Fatalf("bwrap wrote read-only /user source: %v", err)
	}
}

func TestWrapCommandLinuxDoesNotOverrideExplicitMaskedAlias(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	workspace, override := t.TempDir(), t.TempDir()
	readOnlySource := filepath.Join(workspace, "locked", "readonly")
	if err := os.MkdirAll(readOnlySource, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: override, Target: "/workspace/locked", Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: readOnlySource, Target: "/external/readonly", Access: hostlayout.ReadOnly})
	_, args, err := wrapCommand(sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}}, layout, sandboxpkg.PathWorkspace, nil, "", "sh", []string{"-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	bindIndex := func(flag, source, target string) int {
		for i := range args {
			if args[i] == flag && i+2 < len(args) && args[i+1] == source && args[i+2] == target {
				return i
			}
		}
		return -1
	}
	rootIndex, maskedIndex := bindIndex("--bind", workspace, "/workspace"), bindIndex("--bind", override, "/workspace/locked")
	if rootIndex < 0 || maskedIndex < 0 || rootIndex >= maskedIndex {
		t.Fatalf("root/explicit mount order root=%d explicit=%d: %v", rootIndex, maskedIndex, args)
	}
	if aliasIndex := bindIndex("--ro-bind", readOnlySource, "/workspace/locked/readonly"); aliasIndex >= 0 {
		t.Fatalf("derived alias at %d overrides explicit mount at %d: %v", aliasIndex, maskedIndex, args)
	}
	if chdirIndex := slices.Index(args, "--chdir"); chdirIndex < maskedIndex {
		t.Fatalf("--chdir appears before explicit mount: %v", args)
	}
}

func TestWrapCommandLinuxHonorsReadOnlyWorkspaceRootAndChildOrder(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	base := t.TempDir()
	project := filepath.Join(base, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := hostlayout.Layout{WorkspaceSource: project, WorkingDirSource: project, Mounts: []hostlayout.Mount{
		{Source: base, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadOnly},
		{Source: project, Target: sandboxpkg.PathWorkspace + "/project", Access: hostlayout.ReadWrite},
	}}
	_, args, err := wrapCommand(sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}}, layout, "/workspace/project", nil, "", "sh", []string{"-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	index := func(flag, source, target string) int {
		for i := range args {
			if args[i] == flag && i+2 < len(args) && args[i+1] == source && args[i+2] == target {
				return i
			}
		}
		return -1
	}
	rootIndex, childIndex := index("--ro-bind", base, "/workspace"), index("--bind", project, "/workspace/project")
	if rootIndex < 0 || childIndex < 0 || rootIndex >= childIndex {
		t.Fatalf("read-only root/child order root=%d child=%d: %v", rootIndex, childIndex, args)
	}
}

func TestWrapCommandLinuxDoesNotBindWorkspaceFallback(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	base := t.TempDir()
	project := filepath.Join(base, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := hostlayout.Layout{WorkspaceSource: project, WorkingDirSource: project, Mounts: []hostlayout.Mount{
		{Source: project, Target: sandboxpkg.PathWorkspace + "/project", Access: hostlayout.ReadWrite},
	}}
	_, args, err := wrapCommand(sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}}, layout, "/workspace/project", nil, "", "sh", []string{"-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	hasBind := func(source, target string) bool {
		for i := range args {
			if args[i] == "--bind" && i+2 < len(args) && args[i+1] == source && args[i+2] == target {
				return true
			}
		}
		return false
	}
	if hasBind(project, sandboxpkg.PathWorkspace) || !hasBind(project, "/workspace/project") {
		t.Fatalf("workspace fallback broadened child authority: %v", args)
	}
}

func TestWrapCommandLinuxOrdersNestedDeclaredTargets(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	workspace, shallow, deep := t.TempDir(), t.TempDir(), t.TempDir()
	layout := layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: shallow, Target: "/workspace/mount", Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: deep, Target: "/workspace/mount/deep", Access: hostlayout.ReadWrite})
	_, args, err := wrapCommand(sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}}, layout, sandboxpkg.PathWorkspace, nil, "", "sh", []string{"-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	bindIndex := func(source, target string) int {
		for i := range args {
			if args[i] == "--bind" && i+2 < len(args) && args[i+1] == source && args[i+2] == target {
				return i
			}
		}
		return -1
	}
	shallowIndex, deepIndex := bindIndex(shallow, "/workspace/mount"), bindIndex(deep, "/workspace/mount/deep")
	if shallowIndex < 0 || deepIndex < 0 || shallowIndex >= deepIndex {
		t.Fatalf("declared bind order shallow=%d deep=%d: %v", shallowIndex, deepIndex, args)
	}
	chdirIndex := slices.Index(args, "--chdir")
	if chdirIndex < deepIndex {
		t.Fatalf("--chdir appears before declared mounts: %v", args)
	}
}

// TestWrapCommand_linux_twoRoots verifies the two-root mounts: the agent's own
// dir (WorkspaceRoot) is bound at /workspace and the shared user-data root at
// /user, with NO sibling-hiding tmpfs — isolation is by non-mounting, since the
// workspace IS the per-agent dir and siblings are never exposed.
func TestWrapCommand_linux_twoRoots(t *testing.T) {
	skipIfBwrapNotFunctional(t)

	agentDir := "/tmp/test-workspace/users/u1/agents/a1"
	userData := "/tmp/test-workspace/users/u1/data"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: agentDir + "/projects/p",
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	layout := layoutFor(agentDir, filepath.Join(agentDir, "projects", "p"),
		hostlayout.Mount{Source: agentDir, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: userData, Target: sandboxpkg.MountUserData, Access: hostlayout.ReadWrite})
	_, args, err := wrapCommand(policy, layout, "/workspace/projects/p", nil, "", "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasFlagPair := func(flag, first, second string) bool {
		for i, a := range args {
			if a == flag && i+2 < len(args) && args[i+1] == first && args[i+2] == second {
				return true
			}
		}
		return false
	}

	if !hasFlagPair("--bind", agentDir, "/workspace") {
		t.Errorf("expected --bind %s /workspace, got %v", agentDir, args)
	}
	if !hasFlagPair("--bind", userData, "/user") {
		t.Errorf("expected --bind %s /user, got %v", userData, args)
	}
	for i, a := range args {
		if a == "--tmpfs" && i+1 < len(args) && args[i+1] == "/workspace/agents" {
			t.Errorf("two-root layout must not hide siblings with a tmpfs, got %v", args)
		}
	}
}
