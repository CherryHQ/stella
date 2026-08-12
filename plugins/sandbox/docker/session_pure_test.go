package docker

import (
	"os"
	"path"
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

func TestWithServerURL(t *testing.T) {
	t.Run("blank url leaves env untouched", func(t *testing.T) {
		in := map[string]string{"A": "1"}
		got := withServerURL(in, "")
		if _, ok := got["STELLA_SERVER_URL"]; ok {
			t.Fatalf("did not expect STELLA_SERVER_URL: %v", got)
		}
	})
	t.Run("sets url and does not mutate input", func(t *testing.T) {
		in := map[string]string{"A": "1"}
		got := withServerURL(in, "http://stella:25678")
		if got["STELLA_SERVER_URL"] != "http://stella:25678" || got["A"] != "1" {
			t.Fatalf("unexpected: %v", got)
		}
		if _, ok := in["STELLA_SERVER_URL"]; ok {
			t.Fatalf("input map was mutated: %v", in)
		}
	})
	t.Run("nil env", func(t *testing.T) {
		got := withServerURL(nil, "http://stella:25678")
		if got["STELLA_SERVER_URL"] != "http://stella:25678" {
			t.Fatalf("unexpected: %v", got)
		}
	})
}

func TestBuildMountTable(t *testing.T) {
	table := buildMountTable(mountTableOptions{
		WorkspaceHost:  "/host/ws",
		WorkspaceMount: "/container/ws",
		Mounts: []sandboxpkg.Mount{
			{HostPath: "/host/ws", SandboxPath: "/container/ws", Access: sandboxpkg.MountReadWrite},
			{HostPath: "/host/data", SandboxPath: "/user", Access: sandboxpkg.MountReadWrite},
			{HostPath: "/extra/path", SandboxPath: "/extra/path", Access: sandboxpkg.MountReadOnly},
		},
		TempHost: "/tmp/user-1",
	})
	if len(table) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(table), table)
	}
	if table[0].HostPath != "/host/ws" || table[0].ContainerPath != "/container/ws" || table[0].ReadOnly {
		t.Fatalf("unexpected workspace mount: %+v", table[0])
	}
	if table[1].HostPath != "/host/data" || table[1].ContainerPath != "/user" || table[1].ReadOnly {
		t.Fatalf("unexpected user-data mount: %+v", table[1])
	}
	if table[2].HostPath != "/extra/path" || table[2].ContainerPath != "/extra/path" || !table[2].ReadOnly {
		t.Fatalf("unexpected extra mount: %+v", table[2])
	}
	if table[3].HostPath != "/tmp/user-1" || table[3].ContainerPath != "/tmp" || table[3].ReadOnly {
		t.Fatalf("unexpected tmp mount: %+v", table[3])
	}
}

func TestContainerPathNormalizationWithWindowsStylePolicyPaths(t *testing.T) {
	if got, want := cleanContainerPath(`\opt\stella\users\u1\.mise-tools`), "/opt/stella/users/u1/.mise-tools"; got != want {
		t.Fatalf("cleanContainerPath = %q, want %q", got, want)
	}

	table := buildMountTable(mountTableOptions{Mounts: normalizeDockerPolicyMounts([]sandboxpkg.Mount{
		{HostPath: `C:\stella\users\u1`, SandboxPath: `\workspace\`, Access: sandboxpkg.MountReadWrite},
		{HostPath: `C:\stella\users\u1\.mise-tools`, SandboxPath: `\opt\stella\users\u1\.mise-tools`, Access: sandboxpkg.MountReadWrite},
	})})
	if got, want := table[0].ContainerPath, "/workspace"; got != want {
		t.Errorf("workspace ContainerPath = %q, want %q", got, want)
	}
	if got, want := table[1].ContainerPath, "/opt/stella/users/u1/.mise-tools"; got != want {
		t.Errorf("mise ContainerPath = %q, want %q", got, want)
	}

	mounts := nonWorkspacePolicyMounts(normalizeDockerPolicyMounts([]sandboxpkg.Mount{
		{HostPath: `C:\workspace`, SandboxPath: `\workspace`, Access: sandboxpkg.MountReadWrite},
		{HostPath: `C:\stella\bin`, SandboxPath: `\opt\stella\bin`, Access: sandboxpkg.MountReadOnly},
		{HostPath: `C:\user`, SandboxPath: `\user`, Access: sandboxpkg.MountReadWrite},
	}))
	if len(mounts) != 1 || mounts[0].SandboxPath != "/user" {
		t.Fatalf("nonWorkspacePolicyMounts = %+v, want only /user", mounts)
	}

	tools := writableToolTrees(normalizeDockerPolicyMounts([]sandboxpkg.Mount{{
		HostPath:    `C:\stella\users\u1\.mise-tools`,
		SandboxPath: `\opt\stella\users\u1\.mise-tools`,
		Access:      sandboxpkg.MountReadWrite,
	}}))
	if len(tools) != 1 || tools[0].Container != "/opt/stella/users/u1/.mise-tools" {
		t.Fatalf("writableToolTrees = %+v, want normalized mise tree", tools)
	}
	if got, want := path.Base(tools[0].Container), ".mise-tools"; got != want {
		t.Errorf("container base = %q, want %q", got, want)
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

func TestPrepareSessionTempDir(t *testing.T) {
	stellaHome := t.TempDir()
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "host", cfg: Config{RuntimeMode: DockerSandboxModeHost, StellaHome: stellaHome}, want: filepath.Join(stellaHome, "cache", "sandbox-tmp", "sandbox-test-host")},
		{name: "bind", cfg: Config{RuntimeMode: DockerSandboxModeBind, StellaHome: stellaHome, ContainerPathPrefix: stellaHome, HostPathPrefix: "/daemon/stella"}, want: filepath.Join(stellaHome, "cache", "sandbox-tmp", "sandbox-test-bind")},
		{name: "volume", cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}, want: filepath.Join(stellaHome, "cache", "sandbox-tmp", "sandbox-test-volume")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &dockerFactory{cfg: tt.cfg}
			tempDir, err := f.prepareSessionTempDir("sandbox-test-" + tt.name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
			if tempDir != tt.want {
				t.Fatalf("temp dir = %q, want %q", tempDir, tt.want)
			}
			info, err := os.Stat(tempDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o777 || info.Mode()&os.ModeSticky == 0 {
				t.Errorf("temp mode = %v, want sticky 0777", info.Mode())
			}
			parent, err := os.Stat(filepath.Dir(tempDir))
			if err != nil {
				t.Fatal(err)
			}
			if got := parent.Mode().Perm(); got != 0o700 {
				t.Errorf("temp parent mode = %#o, want 0700", got)
			}
		})
	}
}

func TestConfigureSessionMounts_HostMode(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeHost, StellaHome: stellaHome}}
	opts := dockerModeCreateOptions(workspace)
	mountedExtra, mountedTmp, _, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, workspace, extra, tmp), workspace, "", tmp)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != workspace {
		t.Fatalf("WorkspaceHost = %q, want %q", opts.WorkspaceHost, workspace)
	}
	if mountedTmp != tmp {
		t.Fatalf("mounted tmp = %q, want %q", mountedTmp, tmp)
	}
	if len(mountedExtra) < 2 || mountedExtra[1].HostPath != extra {
		t.Fatalf("mounted extra = %v, want [%q]", mountedExtra, extra)
	}
	assertNoMountTo(t, opts.ExtraMounts, filepath.Join(stellaHomeMount, "bin"))
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
	policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
	tempDir := preparedDockerTemp(t, f)
	mountedExtra, mountedTmp, _, err := f.configureSessionMounts(&opts, policy, workspace, "", tempDir)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != "/daemon/stella/users/user" {
		t.Fatalf("WorkspaceHost = %q", opts.WorkspaceHost)
	}
	if mountedTmp != tempDir {
		t.Fatalf("mounted tmp = %q, want session temp %q", mountedTmp, tempDir)
	}
	assertMount(t, opts.ExtraMounts, "/daemon/stella/cache/sandbox-tmp/sandbox-test", "/tmp", false, dockerclient.MountType(""), "")
	if len(mountedExtra) < 2 || mountedExtra[1].HostPath != extra {
		t.Fatalf("mounted extra = %v, want [%q]", mountedExtra, extra)
	}
	assertNoMountTo(t, opts.ExtraMounts, filepath.Join(stellaHomeMount, "bin"))
	assertMount(t, opts.ExtraMounts, "/daemon/stella/users/user/skills", extra, true, dockerclient.MountType(""), "")
	assertMount(t, opts.ExtraMounts, "/daemon/stella/users/user/skills", filepath.Join(workspaceMount, "skills"), true, dockerclient.MountTypeBind, "")
}

func TestConfigureSessionMounts_VolumeModeUsesSubpaths(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	outsideExtra := t.TempDir()
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
	opts := dockerModeCreateOptions(workspace)
	policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
	policy.Filesystem.Mounts = append(policy.Filesystem.Mounts, sandboxpkg.Mount{HostPath: outsideExtra, SandboxPath: outsideExtra, Access: sandboxpkg.MountReadOnly})
	tempDir := preparedDockerTemp(t, f)
	mountedExtra, mountedTmp, _, err := f.configureSessionMounts(&opts, policy, workspace, "", tempDir)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	if opts.WorkspaceHost != "" {
		t.Fatalf("WorkspaceHost = %q, want empty in volume mode", opts.WorkspaceHost)
	}
	if mountedTmp != tempDir {
		t.Fatalf("mounted tmp = %q, want session temp %q", mountedTmp, tempDir)
	}
	assertMount(t, opts.ExtraMounts, "stella-data", "/tmp", false, dockerclient.MountTypeVolume, "cache/sandbox-tmp/sandbox-test")
	if len(mountedExtra) < 2 || mountedExtra[1].HostPath != extra {
		t.Fatalf("mounted extra = %v, want only [%q]", mountedExtra, extra)
	}
	assertMount(t, opts.ExtraMounts, "stella-data", workspaceMount, false, dockerclient.MountTypeVolume, "users/user")
	assertNoMountTo(t, opts.ExtraMounts, filepath.Join(stellaHomeMount, "bin"))
	assertMount(t, opts.ExtraMounts, "stella-data", extra, true, dockerclient.MountTypeVolume, "users/user/skills")
	assertMount(t, opts.ExtraMounts, "stella-data", filepath.Join(workspaceMount, "skills"), true, dockerclient.MountTypeVolume, "users/user/skills")
}

func TestConfigureSessionMounts_VolumeModeRejectsStellaHomeAsWorkspace(t *testing.T) {
	stellaHome, _, extra, tmp := dockerModeTestDirs(t)
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
	opts := dockerModeCreateOptions(stellaHome)
	_, _, _, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, stellaHome, extra, tmp), stellaHome, "", preparedDockerTemp(t, f))
	if err == nil {
		t.Fatal("expected error when volume workspace is STELLA_HOME itself")
	}
	if !strings.Contains(err.Error(), "not STELLA_HOME itself") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigureSessionMounts_RunnerScratchAcceptedByDockerModes(t *testing.T) {
	stellaHome := t.TempDir()
	workspace := filepath.Join(stellaHome, "runner-scratch", "runner-123")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: workspace}}

	t.Run("bind", func(t *testing.T) {
		f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeBind, StellaHome: stellaHome, ContainerPathPrefix: stellaHome, HostPathPrefix: "/daemon/stella"}}
		opts := dockerModeCreateOptions(workspace)
		if _, _, _, err := f.configureSessionMounts(&opts, policy, workspace, "", ""); err != nil {
			t.Fatalf("runner scratch bind planning: %v", err)
		}
		if opts.WorkspaceHost != "/daemon/stella/runner-scratch/runner-123" {
			t.Fatalf("WorkspaceHost = %q", opts.WorkspaceHost)
		}
	})

	t.Run("volume", func(t *testing.T) {
		f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
		opts := dockerModeCreateOptions(workspace)
		if _, _, _, err := f.configureSessionMounts(&opts, policy, workspace, "", ""); err != nil {
			t.Fatalf("runner scratch volume planning: %v", err)
		}
		assertMount(t, opts.ExtraMounts, "stella-data", workspaceMount, false, dockerclient.MountTypeVolume, "runner-scratch/runner-123")
	})
}

func TestConfigureSessionMounts_DoesNotMountHostBuiltinBundle(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	builtin := filepath.Join(stellaHome, "bundles", "revision")
	if err := os.MkdirAll(builtin, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeHost, StellaHome: stellaHome}}
	opts := dockerModeCreateOptions(workspace)
	policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
	policy.Filesystem.Mounts = append(policy.Filesystem.Mounts, sandboxpkg.Mount{HostPath: builtin, SandboxPath: sandboxpkg.MountBuiltinSkills, Access: sandboxpkg.MountReadOnly})
	policy.Env = nil
	_, _, _, err := f.configureSessionMounts(&opts, policy, workspace, "", tmp)
	if err != nil {
		t.Fatalf("configureSessionMounts: %v", err)
	}
	assertNoMountTo(t, opts.ExtraMounts, filepath.Join(stellaHomeMount, "bin"))
	assertNoMountTo(t, opts.ExtraMounts, sandboxpkg.MountBuiltinSkills)
}

// TestConfigureSessionMounts_UserDataRoot verifies the shared user-data root is
// mounted RW at /user — bind mode translates the daemon source, volume mode uses
// the STELLA_HOME-relative subpath.
func TestConfigureSessionMounts_UserDataRoot(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	userData := filepath.Join(stellaHome, "users", "user", "data")
	if err := os.MkdirAll(userData, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("bind", func(t *testing.T) {
		f := &dockerFactory{cfg: Config{
			RuntimeMode:         DockerSandboxModeBind,
			StellaHome:          stellaHome,
			ContainerPathPrefix: stellaHome,
			HostPathPrefix:      "/daemon/stella",
		}}
		opts := dockerModeCreateOptions(workspace)
		if _, _, _, err := f.configureSessionMounts(&opts, dockerModePolicy(stellaHome, workspace, extra, tmp), workspace, userData, tmp); err != nil {
			t.Fatalf("configureSessionMounts: %v", err)
		}
		assertMount(t, opts.ExtraMounts, "/daemon/stella/users/user/data", userDataMount, false, dockerclient.MountType(""), "")
	})

	t.Run("volume", func(t *testing.T) {
		f := &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}
		opts := dockerModeCreateOptions(workspace)
		policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
		if _, _, _, err := f.configureSessionMounts(&opts, policy, workspace, userData, preparedDockerTemp(t, f)); err != nil {
			t.Fatalf("configureSessionMounts: %v", err)
		}
		assertMount(t, opts.ExtraMounts, "stella-data", userDataMount, false, dockerclient.MountTypeVolume, "users/user/data")
	})
}

func TestConfigureSessionMounts_WritableMiseTree(t *testing.T) {
	stellaHome, workspace, extra, tmp := dockerModeTestDirs(t)
	miseDir := filepath.Join(stellaHome, "users", "user", ".mise-tools")
	if err := os.MkdirAll(miseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	containerPath := filepath.Join(stellaHomeMount, "users", "user", ".mise-tools")
	for _, tc := range []struct {
		name       string
		factory    *dockerFactory
		wantSource string
		wantType   dockerclient.MountType
		wantSub    string
	}{
		{name: "bind", factory: &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeBind, StellaHome: stellaHome, ContainerPathPrefix: stellaHome, HostPathPrefix: "/daemon/stella"}}, wantSource: "/daemon/stella/users/user/.mise-tools"},
		{name: "volume", factory: &dockerFactory{cfg: Config{RuntimeMode: DockerSandboxModeVolume, StellaHome: stellaHome, StellaHomeVolume: "stella-data"}}, wantSource: "stella-data", wantType: dockerclient.MountTypeVolume, wantSub: "users/user/.mise-tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := dockerModePolicy(stellaHome, workspace, extra, tmp)
			policy.Filesystem.Mounts = append(policy.Filesystem.Mounts, sandboxpkg.Mount{HostPath: miseDir, SandboxPath: containerPath, Access: sandboxpkg.MountReadWrite})
			tempDir := tmp
			if tc.factory.cfg.RuntimeMode == DockerSandboxModeVolume {
				tempDir = preparedDockerTemp(t, tc.factory)
			}
			opts := dockerModeCreateOptions(workspace)
			if _, _, _, err := tc.factory.configureSessionMounts(&opts, policy, workspace, "", tempDir); err != nil {
				t.Fatal(err)
			}
			assertMount(t, opts.ExtraMounts, tc.wantSource, containerPath, false, tc.wantType, tc.wantSub)
		})
	}
}

// TestTranslateEnvPaths_Mise verifies the per-user MISE_DATA_DIR is rewritten to
// its /opt/stella container view and the MISE_TRUSTED_CONFIG_PATHS list is split,
// translated element-wise, and deduped (keeping the literal /workspace mise needs).
func TestTranslateEnvPaths_Mise(t *testing.T) {
	stellaHome := "/host/.stella"
	sep := string(filepath.ListSeparator)
	mountTable := []dockerclient.Mount{{HostPath: "/host/ws", ContainerPath: "/workspace"}}
	envMaps := []envPathMap{{HostPrefix: stellaHome, ContainerPrefix: stellaHomeMount}}
	env := map[string]string{
		"MISE_DATA_DIR":             stellaHome + "/users/u1/.mise-tools",
		"MISE_TRUSTED_CONFIG_PATHS": strings.Join([]string{stellaHome + "/.mise-tools/configs/_builtin.toml", "/workspace", "/host/ws"}, sep),
		"PATH":                      "/host/leak",
	}
	out := translateEnvPaths(env, mountTable, envMaps)

	if got, want := out["MISE_DATA_DIR"], stellaHomeMount+"/users/u1/.mise-tools"; got != want {
		t.Errorf("MISE_DATA_DIR = %q, want %q", got, want)
	}
	wantTrusted := strings.Join([]string{stellaHomeMount + "/.mise-tools/configs/_builtin.toml", "/workspace"}, sep)
	if got := out["MISE_TRUSTED_CONFIG_PATHS"]; got != wantTrusted {
		t.Errorf("MISE_TRUSTED_CONFIG_PATHS = %q, want %q", got, wantTrusted)
	}
	if _, ok := out["PATH"]; ok {
		t.Error("PATH must be dropped (image-baked PATH wins)")
	}
}

func TestApplyFilesystemEnvUsesMountedUserDataOrWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userData string
		root     string
		tmpDir   string
	}{
		{name: "mounted principal data", userData: "/host/data", root: "/host/data", tmpDir: "/host/tmp/principal"},
		{name: "mounted group data", userData: "/host/data/group-g1", root: "/host/data/group-g1", tmpDir: "/host/tmp/group-g1"},
		{name: "no user-data mount", root: "/host/workspace", tmpDir: "/tmp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{sandboxpkg.EnvXDGRuntimeDir: "/run/user/1000"}
			view := sandboxpkg.FilesystemView{Home: "/host/workspace", SharedDataDir: tc.userData, TempDir: tc.tmpDir}
			if err := sandboxpkg.ApplyFilesystemEnv(env, view); err != nil {
				t.Fatalf("ApplyFilesystemEnv: %v", err)
			}
			for key, want := range map[string]string{
				sandboxpkg.EnvHome:          "/host/workspace",
				sandboxpkg.EnvTempDir:       tc.tmpDir,
				sandboxpkg.EnvXDGConfigHome: filepath.Join(tc.root, ".config"),
				sandboxpkg.EnvXDGDataHome:   filepath.Join(tc.root, ".local", "share"),
				sandboxpkg.EnvXDGStateHome:  filepath.Join(tc.root, ".local", "state"),
				sandboxpkg.EnvXDGCacheHome:  filepath.Join(tc.root, ".cache"),
			} {
				if got := env[key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if tc.userData == "" {
				if _, ok := env[sandboxpkg.EnvStellaAssetsDir]; ok {
					t.Errorf("%s must not be set", sandboxpkg.EnvStellaAssetsDir)
				}
			} else if got, want := env[sandboxpkg.EnvStellaAssetsDir], filepath.Join(tc.userData, "assets"); got != want {
				t.Errorf("STELLA_ASSETS_DIR = %q, want %q", got, want)
			}
			if _, ok := env[sandboxpkg.EnvXDGRuntimeDir]; ok {
				t.Error("XDG_RUNTIME_DIR must not be set")
			}
		})
	}
}

func TestApplyDockerFilesystemEnvRequiresMountedTempDir(t *testing.T) {
	env := map[string]string{sandboxpkg.EnvTempDir: "/stale/tmp"}
	if err := applyDockerFilesystemEnv(env, "/host/workspace", "", ""); err == nil {
		t.Fatal("applyDockerFilesystemEnv accepted an unmounted TMPDIR")
	}
	translated := translateEnvPaths(map[string]string{sandboxpkg.EnvTempDir: "/tmp"}, nil, nil)
	if _, ok := translated[sandboxpkg.EnvTempDir]; ok {
		t.Fatal("unmounted container-local TMPDIR bypassed path translation")
	}
}

func TestApplyDockerFilesystemEnvWithoutUserDataUsesMountedFallbackTemp(t *testing.T) {
	env := map[string]string{
		"STELLA_USER_DIR":             "/stale/user",
		sandboxpkg.EnvStellaAssetsDir: "/stale/user/assets",
	}
	if err := applyDockerFilesystemEnv(env, "/host/workspace", "", "/host/tmp/session"); err != nil {
		t.Fatalf("applyDockerFilesystemEnv: %v", err)
	}
	for _, key := range []string{"STELLA_USER_DIR", sandboxpkg.EnvStellaAssetsDir} {
		if _, ok := env[key]; ok {
			t.Errorf("missing user mount must clear %s", key)
		}
	}
	containerEnv := translateEnvPaths(env, []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: workspaceMount},
		{HostPath: "/host/tmp/session", ContainerPath: "/tmp"},
	}, nil)
	if got, want := containerEnv[sandboxpkg.EnvTempDir], "/tmp"; got != want {
		t.Errorf("container TMPDIR = %q, want %q", got, want)
	}
}

func TestDockerFilesystemEnvCreateAndExecTranslationMatch(t *testing.T) {
	policyEnv := map[string]string{"PERSISTENT_VALUE": "policy"}
	if err := applyDockerFilesystemEnv(policyEnv, "/host/workspace", "/host/user", "/host/tmp"); err != nil {
		t.Fatalf("applyDockerFilesystemEnv: %v", err)
	}
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: workspaceMount},
		{HostPath: "/host/user", ContainerPath: userDataMount},
		{HostPath: "/host/tmp", ContainerPath: "/tmp"},
	}

	// Container creation has no per-call overrides or injected tool path.
	createEnv := translateEnvPaths(mergeEnv(policyEnv, nil), mounts, nil)

	// Exec independently merges request-scoped env before translating, then adds
	// its container-native tool path. This must not alter filesystem semantics.
	execEnv := injectToolPaths(
		translateEnvPaths(mergeEnv(policyEnv, map[string]string{"REQUEST_VALUE": "exec"}), mounts, nil),
		[]string{"/tools/request/bin"},
	)

	for _, key := range []string{
		sandboxpkg.EnvHome,
		sandboxpkg.EnvStellaAssetsDir,
		sandboxpkg.EnvTempDir,
		sandboxpkg.EnvXDGConfigHome,
		sandboxpkg.EnvXDGDataHome,
		sandboxpkg.EnvXDGStateHome,
		sandboxpkg.EnvXDGCacheHome,
	} {
		if got, want := execEnv[key], createEnv[key]; got != want {
			t.Errorf("%s differs between create and exec: got %q, want %q", key, got, want)
		}
	}
	if _, ok := createEnv[sandboxpkg.EnvXDGRuntimeDir]; ok {
		t.Error("create environment must not set XDG_RUNTIME_DIR")
	}
	if _, ok := execEnv[sandboxpkg.EnvXDGRuntimeDir]; ok {
		t.Error("exec environment must not set XDG_RUNTIME_DIR")
	}
	for key, want := range map[string]string{
		sandboxpkg.EnvHome:            workspaceMount,
		sandboxpkg.EnvStellaAssetsDir: userDataMount + "/assets",
		sandboxpkg.EnvTempDir:         "/tmp",
		sandboxpkg.EnvXDGConfigHome:   userDataMount + "/.config",
		sandboxpkg.EnvXDGDataHome:     userDataMount + "/.local/share",
		sandboxpkg.EnvXDGStateHome:    userDataMount + "/.local/state",
		sandboxpkg.EnvXDGCacheHome:    userDataMount + "/.cache",
	} {
		if got := createEnv[key]; got != want {
			t.Errorf("create %s = %q, want %q", key, got, want)
		}
	}
	if got := createEnv["PERSISTENT_VALUE"]; got != "policy" {
		t.Errorf("create PERSISTENT_VALUE = %q, want policy", got)
	}
	if _, ok := createEnv["REQUEST_VALUE"]; ok {
		t.Error("create environment must not include request override")
	}
	if got := execEnv["REQUEST_VALUE"]; got != "exec" {
		t.Errorf("exec REQUEST_VALUE = %q, want exec", got)
	}
	if got, want := execEnv["PATH"], "/tools/request/bin:"+containerDefaultPATH; got != want {
		t.Errorf("exec PATH = %q, want %q", got, want)
	}
	if _, ok := createEnv["PATH"]; ok {
		t.Error("create environment must not inject exec tool PATH")
	}
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

func preparedDockerTemp(t *testing.T, factory *dockerFactory) string {
	t.Helper()
	tempDir, err := factory.prepareSessionTempDir("sandbox-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	return tempDir
}

func dockerModePolicy(stellaHome, workspace, extra, tmp string) sandboxpkg.Policy {
	return sandboxpkg.Policy{
		Env: map[string]string{"STELLA_HOME": stellaHome},
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
			Mounts:        []sandboxpkg.Mount{{HostPath: workspace, SandboxPath: workspaceMount, Access: sandboxpkg.MountReadWrite}, {HostPath: extra, SandboxPath: extra, Access: sandboxpkg.MountReadOnly}, {HostPath: filepath.Join(stellaHome, ".agents", "skills"), SandboxPath: filepath.Join(stellaHomeMount, ".agents", "skills"), Access: sandboxpkg.MountReadOnly}},
		},
	}
}

func assertNoMountTo(t *testing.T, mounts []dockerclient.Mount, target string) {
	t.Helper()
	for _, m := range mounts {
		if m.ContainerPath == target {
			t.Fatalf("expected no mount to %q, found %+v", target, m)
		}
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
