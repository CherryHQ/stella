package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

func TestMergeEnv(t *testing.T) {
	t.Run("nil inputs", func(t *testing.T) {
		got := mergeEnv(nil, nil)
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})
	t.Run("policy only", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "1"}, nil)
		if got["A"] != "1" {
			t.Fatalf("unexpected: %v", got)
		}
	})
	t.Run("opts override policy", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "policy"}, map[string]string{"A": "opts"})
		if got["A"] != "opts" {
			t.Fatalf("expected opts to win, got %q", got["A"])
		}
	})
	t.Run("merge both", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "1"}, map[string]string{"B": "2"})
		if got["A"] != "1" || got["B"] != "2" {
			t.Fatalf("unexpected: %v", got)
		}
	})
}

func TestBuildMountTable(t *testing.T) {
	stellaHome := t.TempDir()
	for _, name := range []string{"bin", ".mise-tools", filepath.Join(".agents", "skills")} {
		if err := os.MkdirAll(filepath.Join(stellaHome, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	table := buildMountTable(mountTableOptions{
		WorkspaceHost:       "/host/ws",
		WorkspaceMount:      "/container/ws",
		StellaHomeHost:      stellaHome,
		StellaHomeContainer: "/home/stella/.stella",
	})
	if len(table) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(table))
	}
	if table[0].HostPath != "/host/ws" || table[0].ContainerPath != "/container/ws" {
		t.Fatalf("unexpected workspace mount: %+v", table[0])
	}
	if table[0].ReadOnly {
		t.Fatal("workspace should be read-write")
	}
	if table[1].HostPath != filepath.Join(stellaHome, "bin") || table[1].ContainerPath != "/home/stella/.stella/bin" || !table[1].ReadOnly {
		t.Fatalf("unexpected stella bin mount: %+v", table[1])
	}
	if table[2].HostPath != filepath.Join(stellaHome, ".mise-tools") || table[2].ContainerPath != "/home/stella/.stella/.mise-tools" || !table[2].ReadOnly {
		t.Fatalf("unexpected stella mise-tools mount: %+v", table[2])
	}
	if table[3].HostPath != filepath.Join(stellaHome, ".agents/skills") || table[3].ContainerPath != "/home/stella/.stella/.agents/skills" || !table[3].ReadOnly {
		t.Fatalf("unexpected stella skills mount: %+v", table[3])
	}
	// Verify that missing subdirs are skipped — fresh install without .mise-tools.
	partialHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partialHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	tablePartial := buildMountTable(mountTableOptions{
		WorkspaceHost:       "/host/ws",
		WorkspaceMount:      "/container/ws",
		StellaHomeHost:      partialHome,
		StellaHomeContainer: "/home/stella/.stella",
	})
	// workspace + bin only (no .mise-tools, no .agents/skills)
	if len(tablePartial) != 2 {
		t.Fatalf("expected 2 entries for partial stella home, got %d", len(tablePartial))
	}

	tableExtra := buildMountTable(mountTableOptions{
		WorkspaceHost:       "/host/ws",
		WorkspaceMount:      "/container/ws",
		ExtraReadOnlyMounts: []string{"/extra/path"},
		TempDirHost:         "/tmp/user-1",
	})
	if len(tableExtra) != 3 {
		t.Fatalf("expected 3 entries with extra and tmp, got %d", len(tableExtra))
	}
	if tableExtra[1].HostPath != "/extra/path" || tableExtra[1].ContainerPath != "/extra/path" || !tableExtra[1].ReadOnly {
		t.Fatalf("unexpected extra mount: %+v", tableExtra[1])
	}
	if tableExtra[2].HostPath != "/tmp/user-1" || tableExtra[2].ContainerPath != "/tmp" || tableExtra[2].ReadOnly {
		t.Fatalf("unexpected tmp mount: %+v", tableExtra[2])
	}
}

func TestToHostPath(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/ws", ContainerPath: "/workspace"},
		{HostPath: "/tmp/user-1", ContainerPath: "/tmp"},
	}
	tests := []struct {
		name          string
		containerPath string
		wantHost      string
		wantOK        bool
	}{
		{"sub-path", "/tmp/a.txt", "/tmp/user-1/a.txt", true},
		{"exact mount root", "/tmp", "/tmp/user-1", true},
		{"workspace sub-path", "/workspace/foo", "/host/ws/foo", true},
		{"no match", "/etc/passwd", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toHostPath(mounts, tt.containerPath)
			if ok != tt.wantOK || got != tt.wantHost {
				t.Fatalf("toHostPath(%q) = %q, %v; want %q, %v", tt.containerPath, got, ok, tt.wantHost, tt.wantOK)
			}
		})
	}
	t.Run("deepest mount wins", func(t *testing.T) {
		nested := []dockerclient.Mount{
			{HostPath: "/host/tmp", ContainerPath: "/tmp"},
			{HostPath: "/host/tmp/sub", ContainerPath: "/tmp/sub"},
		}
		got, ok := toHostPath(nested, "/tmp/sub/file.txt")
		if !ok || got != "/host/tmp/sub/file.txt" {
			t.Fatalf("toHostPath = %q, %v; want /host/tmp/sub/file.txt, true", got, ok)
		}
	})
}

func TestMapNetworkMode(t *testing.T) {
	cases := []struct {
		mode sandboxpkg.NetworkMode
		want dockerclient.NetworkMode
	}{
		{sandboxpkg.NetworkDisabled, dockerclient.NetworkDisabled},
		{sandboxpkg.NetworkAllowAll, dockerclient.NetworkAllowAll},
	}
	for _, c := range cases {
		policy := sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: c.mode}}
		got := mapNetworkMode(policy)
		if got != c.want {
			t.Fatalf("mode %v: got %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestInjectToolPaths_PrependedWhenSet(t *testing.T) {
	env := map[string]string{"PATH": "/usr/bin:/bin"}
	got := injectToolPaths(env, []string{"/home/stella/.stella-tools/bin"})
	want := "/home/stella/.stella-tools/bin:/usr/bin:/bin"
	if got["PATH"] != want {
		t.Errorf("PATH = %q, want %q", got["PATH"], want)
	}
}

func TestInjectToolPaths_UsesDefaultPathWhenPATHAbsent(t *testing.T) {
	got := injectToolPaths(map[string]string{}, []string{"/home/stella/.stella-tools/bin"})
	if got["PATH"] == "" {
		t.Fatal("PATH should not be empty when tool paths are set")
	}
	if got["PATH"][:len("/home/stella/.stella-tools/bin:")] != "/home/stella/.stella-tools/bin:" {
		t.Errorf("PATH does not start with user tool bin: %q", got["PATH"])
	}
	if len(got["PATH"]) <= len("/home/stella/.stella-tools/bin:") {
		t.Error("PATH should include containerDefaultPATH after user tool bin")
	}
}

func TestInjectToolPaths_NoOpWhenEmpty(t *testing.T) {
	env := map[string]string{"PATH": "/usr/bin:/bin"}
	got := injectToolPaths(env, nil)
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH changed when tool paths absent: %q", got["PATH"])
	}
}

func TestConfigureSessionMounts_HostMode(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeHost, StellaHome: stellaHome}}
	opts := dockerModeCreateOptions(workspace)
	mountedExtra, mountedTmp, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, workspace, extra, tmp), workspace)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != workspace {
		t.Fatalf("WorkspaceHost = %q, want %q", opts.WorkspaceHost, workspace)
	}
	if mountedTmp != tmp {
		t.Fatalf("mounted tmp = %q, want %q", mountedTmp, tmp)
	}
	if len(mountedExtra) != 1 || mountedExtra[0] != extra {
		t.Fatalf("mounted extra = %v, want [%q]", mountedExtra, extra)
	}
	assertMount(t, opts.ExtraMounts, filepath.Join(stellaHome, "bin"), filepath.Join(stellaHomeMount, "bin"), true, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, tmp, "/tmp", false, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, extra, extra, true, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, extra, filepath.Join(workspaceMount, "skills"), true, dockerclient.MountTypeBind, "")
}

func TestConfigureSessionMounts_BindModeTranslatesSources(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{
		RuntimeMode:         DockerSandboxModeBind,
		StellaHome:          stellaHome,
		ContainerPathPrefix: stellaHome,
		HostPathPrefix:      "/daemon/stella",
	}}
	opts := dockerModeCreateOptions(workspace)
	mountedExtra, mountedTmp, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, workspace, extra, tmp), workspace)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != "/daemon/stella/users/user" {
		t.Fatalf("WorkspaceHost = %q", opts.WorkspaceHost)
	}
	if mountedTmp != "" {
		t.Fatalf("tmp outside STELLA_HOME should be skipped in bind mode, got %q", mountedTmp)
	}
	if len(mountedExtra) != 1 || mountedExtra[0] != extra {
		t.Fatalf("mounted extra = %v, want [%q]", mountedExtra, extra)
	}
	assertMount(t, opts.ExtraMounts, "/daemon/stella/bin", filepath.Join(stellaHomeMount, "bin"), true, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, "/daemon/stella/users/user/skills", extra, true, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, "/daemon/stella/users/user/skills", filepath.Join(workspaceMount, "skills"), true, dockerclient.MountTypeBind, "")
}

func TestConfigureSessionMounts_VolumeModeUsesSubpaths(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	outsideExtra := t.TempDir()
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
	opts := dockerModeCreateOptions(workspace)
	policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
	policy.Filesystem.ExtraReadOnlyMounts = append(policy.Filesystem.ExtraReadOnlyMounts, outsideExtra)
	mountedExtra, mountedTmp, err := f.configureSessionMounts(&opts, policy, workspace)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != "" {
		t.Fatalf("WorkspaceHost = %q, want empty in volume mode", opts.WorkspaceHost)
	}
	if mountedTmp != "" {
		t.Fatalf("volume mode should not mount process-view tmp, got %q", mountedTmp)
	}
	if len(mountedExtra) != 1 || mountedExtra[0] != extra {
		t.Fatalf("mounted extra = %v, want only [%q]", mountedExtra, extra)
	}
	assertMount(t, opts.ExtraMounts, "stella-data", workspaceMount, false, dockerclient.MountTypeVolume, "users/user")
	assertMount(t, opts.ExtraMounts, "stella-data", filepath.Join(stellaHomeMount, "bin"), true, dockerclient.MountTypeVolume, "bin")
	assertMount(t, opts.ExtraMounts, "stella-data", extra, true, dockerclient.MountTypeVolume, "users/user/skills")
	assertMount(t, opts.ExtraMounts, "stella-data", filepath.Join(workspaceMount, "skills"), true, dockerclient.MountTypeVolume, "users/user/skills")
}

func TestConfigureSessionMounts_VolumeModeRejectsStellaHomeAsWorkspace(t *testing.T) {
	stellaHome, _, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
	opts := dockerModeCreateOptions(stellaHome)
	_, _, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, stellaHome, extra, tmp), stellaHome)
	if err == nil {
		t.Fatal("expected error when volume workspace is STELLA_HOME itself")
	}
	if !strings.Contains(err.Error(), "not STELLA_HOME itself") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigureSessionMounts_BindModeUsesConfigStellaHome(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeHost, StellaHome: stellaHome}}
	opts := dockerModeCreateOptions(workspace)
	policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
	policy.Env = nil
	_, _, err := f.configureSessionMounts(&opts, policy, workspace)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	assertMount(t, opts.ExtraMounts, filepath.Join(stellaHome, "bin"), filepath.Join(stellaHomeMount, "bin"), true, dockerclient.MountType(""), "")
}

func dockerModeTestDirs(t *testing.T) (stellaHome, workspace, extra, tmp string) {
	t.Helper()
	stellaHome = t.TempDir()
	workspace = filepath.Join(stellaHome, "users", "user")
	extra = filepath.Join(workspace, "skills")
	tmp = t.TempDir()
	for _, dir := range []string{filepath.Join(stellaHome, "bin"), workspace, extra} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stellaHome, workspace, extra, tmp
}

func dockerModeCreateOptions(workspace string) dockerclient.CreateOptions {
	return dockerclient.CreateOptions{WorkspaceHost: workspace, WorkspaceMount: workspaceMount}
}

func dockerModePolicy(stellaHome, workspace, extra, tmp string) sandboxpkg.Policy {
	return sandboxpkg.Policy{
		Env: map[string]string{"STELLA_HOME": stellaHome},
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot:       workspace,
			WorkingDir:          workspace,
			ExtraReadOnlyMounts: []string{extra},
			TempDirHost:         tmp,
		},
	}
}

func assertMount(t *testing.T, mounts []dockerclient.Mount, source, target string, readOnly bool, mountType dockerclient.MountType, volumeSubpath string) {
	t.Helper()
	for _, m := range mounts {
		if m.HostPath == source && m.ContainerPath == target {
			if m.ReadOnly != readOnly || m.Type != mountType || m.VolumeSubpath != volumeSubpath {
				t.Fatalf("mount %+v flags mismatch; want readOnly=%v type=%q subpath=%q", m, readOnly, mountType, volumeSubpath)
			}
			return
		}
	}
	t.Fatalf("mount %q -> %q not found in %+v", source, target, mounts)
}
