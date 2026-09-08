package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// TestPreparePluginBinariesRealDocker exercises the retained-container seam
// against a real daemon. It is opt-in because it creates sibling containers and
// named volumes. The installer is replaced with a fixture publisher so the
// test measures Docker projection/mount behavior without network or mise I/O.
func TestPreparePluginBinariesRealDocker(t *testing.T) {
	if os.Getenv("STELLA_DOCKER_REAL_PREPARE") != "1" {
		t.Skip("set STELLA_DOCKER_REAL_PREPARE=1 to run the retained Docker session smoke test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	client, err := dockerclient.New()
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	defer func() { _ = client.Close() }()
	image := os.Getenv("STELLA_DOCKER_REAL_PREPARE_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if _, err := client.Version(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	if err := Preflight(ctx, PreflightConfig{Docker: Config{Image: image}}); err != nil {
		t.Skipf("docker image unavailable: %v", err)
	}

	resetToolCacheStateForTest()
	defer resetToolCacheStateForTest()
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	var fixtureVolumes []string
	fixtureNames := make(map[string]string)
	var fixtureMu sync.Mutex
	installSelectionToolCacheFn = func(ctx context.Context, client *dockerclient.Client, cfg Config, imageID, _ string, installerName string, cache *selectionToolCache) (*selectionToolCache, error) {
		originalVolume := cache.VolumeName
		fixtureMu.Lock()
		if existing := fixtureNames[originalVolume]; existing != "" {
			cache.VolumeName = existing
			fixtureMu.Unlock()
			setFixtureEvidence(cache, cfg.SelectionToolBinaries)
			return cache, nil
		}
		index := len(fixtureNames)
		cache.VolumeName = fmt.Sprintf("stella-real-selection-%s-%d", nonce, index)
		cache.MaskVolumeName = fmt.Sprintf("stella-real-selection-mask-%s-%d", nonce, index)
		fixtureNames[originalVolume] = cache.VolumeName
		fixtureMu.Unlock()
		volumes, err := client.VolumeList(ctx, mobyclient.VolumeListOptions{Filters: mobyclient.Filters{}.Add("name", cache.VolumeName)})
		if err != nil {
			return nil, fmt.Errorf("list fixture volume: %w", err)
		}
		for _, volume := range volumes.Items {
			if volume.Name == cache.VolumeName {
				return nil, fmt.Errorf("fixture volume already exists: %s", cache.VolumeName)
			}
		}
		if _, err := client.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{Name: cache.VolumeName, Labels: map[string]string{toolCacheLabel: "true", toolCacheKindLabel: "real-test"}}); err != nil {
			return nil, fmt.Errorf("create fixture volume: %w", err)
		}
		fixtureMu.Lock()
		fixtureVolumes = append(fixtureVolumes, cache.VolumeName)
		fixtureMu.Unlock()
		containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
			Image: imageID, Name: installerName + "-real-test", User: "root", NetworkMode: dockerclient.NetworkDisabled,
			ExtraMounts: []dockerclient.Mount{{HostPath: cache.VolumeName, ContainerPath: cache.RootPath, ReadOnly: false, Type: dockerclient.MountTypeVolume}},
		})
		if err != nil {
			return nil, err
		}
		defer func() { _ = client.Stop(context.Background(), containerID) }()
		var script strings.Builder
		script.WriteString("set -eu\nmkdir -p ")
		script.WriteString(shellQuote(cache.RootPath + "/bin"))
		script.WriteByte('\n')
		for _, binary := range cfg.SelectionToolBinaries {
			script.WriteString("cat > ")
			script.WriteString(shellQuote(cache.RootPath + "/bin/" + binary.Name))
			script.WriteString(" <<'STELLA_FIXTURE'\n#!/bin/sh\nprintf '%s\\n' ")
			script.WriteString(shellQuote(binary.Name + "-" + binary.Version))
			script.WriteString("\nSTELLA_FIXTURE\nchmod 0755 ")
			script.WriteString(shellQuote(cache.RootPath + "/bin/" + binary.Name))
			script.WriteByte('\n')
		}
		script.WriteString(": > ")
		script.WriteString(shellQuote(cache.RootPath + "/.stella-selection-ready"))
		script.WriteByte('\n')
		result, err := client.Exec(ctx, dockerclient.ExecOptions{ContainerID: containerID, Command: []string{"/bin/sh", "-s"}, Cwd: cache.RootPath, Stdin: strings.NewReader(script.String())})
		if err != nil {
			return nil, err
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("fixture publisher exited with %d: %s", result.ExitCode, result.Stderr)
		}
		setFixtureEvidence(cache, cfg.SelectionToolBinaries)
		return cache, nil
	}

	workspace := t.TempDir()
	stableRoot := filepath.Join(t.TempDir(), "public-session")
	factory := &dockerFactory{
		cfg: Config{
			Image: image, RuntimeMode: DockerSandboxModeHost,
			SelectionToolBinaries: []ToolBinary{{PluginID: "plugin/a", ConfigID: "cfg/plugin/a", Scope: "user", Revision: 1, PackageDigest: "digest/plugin/a", Name: "tool-a", Tool: "uv", Version: "a"}},
			StableProjectionID:    "real-docker-" + nonce, StableProjectionHostRoot: stableRoot,
		},
		mountSources: map[string]string{sandboxpkg.MountWorkspace: workspace},
		clientFn:     func() (*dockerclient.Client, error) { return client, nil },
	}
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: sandboxpkg.MountWorkspace, Mounts: []sandboxpkg.Mount{{SandboxPath: sandboxpkg.MountWorkspace, Access: sandboxpkg.MountReadWrite}}}, Env: map[string]string{"OLD_TOKEN": "synthetic"}, Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled}}
	var session sandboxpkg.Session
	var raw *dockerSession
	defer func() {
		if session != nil {
			if err := session.Close(); err != nil {
				t.Logf("close real session: %v", err)
				return
			}
		} else {
			return
		}
		for _, volume := range fixtureVolumes {
			_ = client.VolumeRemove(context.Background(), volume, mobyclient.VolumeRemoveOptions{})
		}
		if raw != nil && raw.stableProjection != nil {
			_ = client.VolumeRemove(context.Background(), raw.stableProjection.VolumeName, mobyclient.VolumeRemoveOptions{})
		}
	}()
	session, err = factory.CreateSession(ctx, policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	raw = session.(*dockerSession)
	containerID := raw.containerID
	prep := func(id, name, version string) ([]string, pkgplugins.PluginPreparationResult) {
		r, paths, err := raw.PreparePluginBinaries(ctx, []pkgplugins.PluginBinarySpec{{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: id, ConfigID: "cfg/" + id, Scope: "user", Revision: 1}, PackageDigest: "digest/" + id, Name: name, Tool: "uv", Version: version}})
		if err != nil {
			t.Fatalf("PreparePluginBinaries(%s): %v", id, err)
		}
		if len(paths) != 1 {
			t.Fatalf("paths(%s) = %v, want one package path", id, paths)
		}
		return paths, r
	}
	pathA, resultA := prep("plugin/a", "tool-a", "a")
	if !resultA.Status("plugin/a").Ready || len(resultA.Binaries) != 1 || resultA.Binaries[0].ResolvedVersion != "a" {
		t.Fatalf("preparation A = %+v", resultA)
	}
	pathB, resultB := prep("plugin/b", "tool-b", "b")
	if !resultB.Status("plugin/b").Ready || len(resultB.Binaries) != 1 || resultB.Binaries[0].ResolvedVersion != "b" {
		t.Fatalf("preparation B = %+v", resultB)
	}
	if raw.containerID != containerID {
		t.Fatalf("container ID changed from %q to %q", containerID, raw.containerID)
	}
	initial, err := raw.Exec(ctx, `test "$OLD_TOKEN" = synthetic`, sandboxpkg.ExecOptions{})
	if err != nil || initial.ExitCode != 0 {
		t.Fatalf("initial policy token missing: %+v, %v", initial, err)
	}
	turnEnv, err := sandboxpkg.RenderEnv(ctx, raw, map[string]string{"CURRENT": "yes"})
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	for key, want := range map[string]string{
		sandboxpkg.EnvHome:          sandboxpkg.MountWorkspace,
		sandboxpkg.EnvTempDir:       "/tmp",
		sandboxpkg.EnvXDGConfigHome: sandboxpkg.MountWorkspace + "/.config",
	} {
		if turnEnv[key] != want {
			t.Fatalf("turn env %s = %q, want %q", key, turnEnv[key], want)
		}
	}
	if _, ok := turnEnv["OLD_TOKEN"]; ok {
		t.Fatal("retained policy token leaked into rendered turn env")
	}
	checkUnset := func(command string) {
		result, execErr := raw.Exec(ctx, command, sandboxpkg.ExecOptions{Env: turnEnv, EnvMode: sandboxpkg.EnvReplace})
		if execErr != nil || result.ExitCode != 0 {
			t.Fatalf("unset env exec = %+v, %v", result, execErr)
		}
	}
	checkUnset(`test -z "${OLD_TOKEN+x}" && test "$HOME" = /workspace && test "$TMPDIR" = /tmp && test "$XDG_CONFIG_HOME" = /workspace/.config`)
	envProcess, err := raw.StartProcess(ctx, sandboxpkg.ProcessRequest{Path: "/bin/sh", Args: []string{"-c", `test -z "${OLD_TOKEN+x}" && test "$HOME" = /workspace && test "$TMPDIR" = /tmp && test "$XDG_CONFIG_HOME" = /workspace/.config`}, Env: turnEnv, EnvMode: sandboxpkg.EnvReplace})
	if err != nil {
		t.Fatalf("StartProcess unset env: %v", err)
	}
	if result, waitErr := envProcess.Wait(ctx); waitErr != nil || result.ExitCode != 0 {
		t.Fatalf("StartProcess unset env = %+v, %v", result, waitErr)
	}

	run := func(command string, paths []string) sandboxpkg.ExecResult {
		result, err := raw.Exec(ctx, command, sandboxpkg.ExecOptions{Env: map[string]string{sandboxpkg.EnvNativeSelectionDir: strings.Join(paths, string(filepath.ListSeparator))}, EnvMode: sandboxpkg.EnvReplace})
		if err != nil {
			t.Fatalf("exec %q: %v", command, err)
		}
		return result
	}
	if got := run("tool-a", pathA).Stdout; !strings.Contains(got, "tool-a-a") {
		t.Fatalf("tool A output = %q", got)
	}
	process, err := raw.StartProcess(ctx, sandboxpkg.ProcessRequest{Path: "tool-b", Env: map[string]string{sandboxpkg.EnvNativeSelectionDir: strings.Join(pathB, string(filepath.ListSeparator))}, EnvMode: sandboxpkg.EnvReplace})
	if err != nil {
		t.Fatalf("StartProcess tool B: %v", err)
	}
	processOutput, readErr := io.ReadAll(process.Stdout())
	if readErr != nil || !strings.Contains(string(processOutput), "tool-b-b") {
		t.Fatalf("tool B output = %q, read error = %v", processOutput, readErr)
	}
	if result, err := process.Wait(ctx); err != nil || result.ExitCode != 0 {
		t.Fatalf("tool B process = %+v, %v", result, err)
	}
	if result := run("test -x "+shellQuote(filepath.Join(pathA[0], "tool-a")), pathB); result.ExitCode != 0 {
		t.Fatalf("old A path is no longer readable: %+v", result)
	}
	if result := run("command -v tool-a >/dev/null 2>&1", pathB); result.ExitCode == 0 {
		t.Fatal("tool A remained on PATH while tool B was selected")
	}
	if result := run("echo leaked > "+shellQuote(filepath.Join(pathB[0], "should-fail")), pathB); result.ExitCode == 0 {
		t.Fatal("read-only projection accepted a write")
	}
	privateStateChecks := []string{
		filepath.Join(containerStableSelectionRoot, ".mise-config"),
		filepath.Join(containerStableSelectionRoot, ".mise-cache"),
		filepath.Join(containerStableSelectionRoot, ".mise-state"),
		filepath.Join(pathB[0], ".mise-config"),
		filepath.Join(pathB[0], ".mise-cache"),
		filepath.Join(pathB[0], ".mise-state"),
	}
	privateCommand := make([]string, 0, len(privateStateChecks))
	for _, path := range privateStateChecks {
		privateCommand = append(privateCommand, "test ! -e "+shellQuote(path))
	}
	if result := run(strings.Join(privateCommand, " && "), pathB); result.ExitCode != 0 {
		t.Fatalf("private installer state visible: %+v", result)
	}
	_, pathsEmpty, err := raw.PreparePluginBinaries(ctx, nil)
	if err != nil || pathsEmpty != nil {
		t.Fatalf("empty preparation = paths %v, err %v", pathsEmpty, err)
	}
	if result := run("command -v tool-a >/dev/null 2>&1 || command -v tool-b >/dev/null 2>&1", nil); result.ExitCode == 0 {
		t.Fatal("empty selection retained an optional executable on PATH")
	}
	if result := run("test -x "+shellQuote(filepath.Join(pathA[0], "tool-a")), nil); result.ExitCode != 0 {
		t.Fatalf("old A absolute path became unreadable after empty selection: %+v", result)
	}
}

func setFixtureEvidence(cache *selectionToolCache, binaries []ToolBinary) {
	cache.Evidence = pkgplugins.BinaryInstallEvidence{}
	for _, binary := range binaries {
		lookup := systemLookupName(binary)
		requested := binary.Version
		if requested == "" {
			requested = "latest"
		}
		cache.Evidence.Tools = append(cache.Evidence.Tools, pkgplugins.BinaryEvidenceTool{Key: binary.Tool, Lookup: lookup, PublicName: binary.Name, RequestedVersion: requested, ResolvedVersion: binary.Version})
	}
}
