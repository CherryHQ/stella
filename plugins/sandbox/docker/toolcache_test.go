package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

func TestSelectionMiseTOMLRegistryTool(t *testing.T) {
	got, err := selectionMiseTOML([]ToolBinary{{Name: "uv", Tool: "uv"}})
	if err != nil {
		t.Fatalf("selectionMiseTOML: %v", err)
	}
	want := `uv = 'latest'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected registry tool form %q in:\n%s", want, got)
	}
}

func TestValidateSelectionCandidatesChecksAllAliasesBeforeInstall(t *testing.T) {
	good := ToolBinary{Name: "good", Tool: "uv", Version: "1"}
	bad := ToolBinary{Name: "bad/name", Tool: "bun", Version: "1"}
	if err := validateSelectionCandidates([]ToolBinary{good, bad}); err == nil {
		t.Fatal("validateSelectionCandidates accepted an unsafe alias")
	}
	if err := validateSelectionCandidates([]ToolBinary{{Name: "xberg", Tool: "github:example/xberg"}}); err == nil {
		t.Fatal("validateSelectionCandidates accepted a core runtime collision")
	}
}

func TestEnsureSelectionToolCacheSetKeepsOptionalPackageFailures(t *testing.T) {
	resetToolCacheStateForTest()
	defer resetToolCacheStateForTest()
	var (
		runtimes   []string
		runtimesMu sync.Mutex
	)
	installSelectionToolCacheFn = func(_ context.Context, _ *dockerclient.Client, cfg Config, _ string, _ string, _ string, cache *selectionToolCache) (*selectionToolCache, error) {
		runtimesMu.Lock()
		runtimes = append(runtimes, cfg.Runtime)
		runtimesMu.Unlock()
		if len(cfg.SelectionToolBinaries) > 0 && cfg.SelectionToolBinaries[0].PluginID == "broken" {
			return nil, errors.New("installation failed")
		}
		return cache, nil
	}
	binaries := []ToolBinary{
		{PluginID: "broken", ConfigID: "broken-config", Scope: "user", Revision: 1, PackageDigest: "sha256:broken", Name: "broken", Tool: "uv", Version: "1"},
		{PluginID: "good", ConfigID: "good-config", Scope: "user", Revision: 1, PackageDigest: "sha256:good", Name: "good", Tool: "bun", Version: "1"},
	}
	set, err := ensureSelectionToolCacheSet(context.Background(), dockerclient.NewWithAPI(noopAPI{}), Config{Runtime: "runsc", SelectionToolBinaries: binaries}, "sha256:image")
	if err != nil {
		t.Fatalf("ensureSelectionToolCacheSet: %v", err)
	}
	if len(set.Packages) != 1 || set.Preparation.SuccessfulPackages[0].PluginID != "good" {
		t.Fatalf("successful packages = %+v, want good only", set.Preparation.SuccessfulPackages)
	}
	if len(set.Preparation.FailedPackages) != 1 || set.Preparation.FailedPackages[0].Package.PluginID != "broken" {
		t.Fatalf("failed packages = %+v, want broken only", set.Preparation.FailedPackages)
	}
	if set.Packages[0].RootPath == containerSelectionRoot || set.Packages[0].BinPath == containerSelectionBin {
		t.Fatalf("optional package reused core selection paths: %+v", set.Packages[0])
	}
	if len(runtimes) != 3 || runtimes[0] != "runsc" || runtimes[1] != "runsc" || runtimes[2] != "runsc" {
		t.Fatalf("helper runtimes = %v, want runsc for core and both packages", runtimes)
	}
}

func TestEnsureSelectionToolCacheSetBoundsPackagePreparation(t *testing.T) {
	resetToolCacheStateForTest()
	defer resetToolCacheStateForTest()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	var (
		inFlight    atomic.Int32
		maxSeen     atomic.Int32
		started     = make(chan struct{})
		release     = make(chan struct{})
		startOnce   sync.Once
		releaseOnce sync.Once
	)
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	defer closeRelease()
	installSelectionToolCacheFn = func(ctx context.Context, _ *dockerclient.Client, cfg Config, _ string, _ string, _ string, cache *selectionToolCache) (*selectionToolCache, error) {
		if len(cfg.SelectionToolBinaries) == 0 {
			return cache, nil
		}
		current := inFlight.Add(1)
		for {
			previous := maxSeen.Load()
			if current <= previous || maxSeen.CompareAndSwap(previous, current) {
				break
			}
		}
		if current == maxConcurrentPackageCaches {
			startOnce.Do(func() { close(started) })
		}
		defer inFlight.Add(-1)
		select {
		case <-release:
			return cache, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	binaries := make([]ToolBinary, 8)
	for i := range binaries {
		binaries[i] = ToolBinary{
			PluginID: fmt.Sprintf("plugin-%d", i), Name: fmt.Sprintf("tool-%d", i), Tool: "uv", Version: "1",
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := ensureSelectionToolCacheSet(ctx, dockerclient.NewWithAPI(noopAPI{}), Config{Runtime: "runsc", SelectionToolBinaries: binaries}, "sha256:image")
		result <- err
	}()

	reachedBound := false
	select {
	case <-started:
		reachedBound = true
	case <-ctx.Done():
	}
	closeRelease()
	err := <-result
	if !reachedBound {
		t.Fatalf("package preparation did not reach the concurrency bound: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("ensureSelectionToolCacheSet: %v", err)
	}
	if got := maxSeen.Load(); got != maxConcurrentPackageCaches {
		t.Fatalf("maximum package concurrency = %d, want %d", got, maxConcurrentPackageCaches)
	}
}

type packageMountRecordingAPI struct {
	noopAPI
	createOpts mobyclient.ContainerCreateOptions
}

func (f *packageMountRecordingAPI) ImageInspect(context.Context, string, ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
	return mobyclient.ImageInspectResult{InspectResponse: image.InspectResponse{ID: "sha256:image"}}, nil
}

func (f *packageMountRecordingAPI) ContainerCreate(_ context.Context, opts mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
	f.createOpts = opts
	return mobyclient.ContainerCreateResult{ID: "sandbox"}, nil
}

func TestCreateSessionMountsOnlySuccessfulPackageCaches(t *testing.T) {
	resetToolCacheStateForTest()
	defer resetToolCacheStateForTest()
	installSelectionToolCacheFn = func(_ context.Context, _ *dockerclient.Client, cfg Config, _ string, _ string, _ string, cache *selectionToolCache) (*selectionToolCache, error) {
		if len(cfg.SelectionToolBinaries) > 0 && cfg.SelectionToolBinaries[0].PluginID == "broken" {
			return nil, errors.New("installation failed")
		}
		return cache, nil
	}
	binaries := []ToolBinary{
		{PluginID: "broken", ConfigID: "broken-config", Scope: "user", Revision: 1, PackageDigest: "sha256:broken", Name: "broken", Tool: "uv", Version: "1"},
		{PluginID: "good", ConfigID: "good-config", Scope: "user", Revision: 1, PackageDigest: "sha256:good", Name: "good", Tool: "bun", Version: "1"},
	}
	api := &packageMountRecordingAPI{}
	workspace := t.TempDir()
	factory := &dockerFactory{
		cfg: Config{
			Image: "test:latest", RuntimeMode: DockerSandboxModeHost, SelectionToolBinaries: binaries,
			SessionEnvRollbacks: map[string]pkgplugins.SessionEnvRollback{
				"BROKEN_STATIC": {PluginID: "broken"},
				"BROKEN_SECRET": {PluginID: "broken", PriorPresent: true, PriorValue: "vault-value"},
				"BROKEN_OAUTH":  {PluginID: "broken"},
				"BROKEN_PATH":   {PluginID: "broken"},
				"BASH_ENV":      {PluginID: "broken", PriorPresent: true, PriorValue: filepath.Join(workspace, "vault-env")},
			},
		},
		mountSources: map[string]string{sandboxpkg.MountWorkspace: workspace},
		clientFn:     func() (*dockerclient.Client, error) { return dockerclient.NewWithAPI(api), nil },
	}
	session, err := factory.CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: sandboxpkg.MountWorkspace},
		Env:        map[string]string{"BROKEN_STATIC": "static-value", "BROKEN_SECRET": "vault-value", "STELLA_HOME": "/runner/stella-home", "TMPDIR": "/package-owned-tmp", "BROKEN_OAUTH": "oauth-value", "BROKEN_PATH": "/package/path", "BASH_ENV": filepath.Join(workspace, "package-env"), "GOOD_SECRET": "good-value"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var packageMounts []mount.Mount
	for _, mount := range api.createOpts.HostConfig.Mounts {
		if strings.HasPrefix(mount.Target, containerPackageSelectionRoot+"/") {
			packageMounts = append(packageMounts, mount)
		}
	}
	if len(packageMounts) != 1 {
		t.Fatalf("package mounts = %+v, want exactly the successful package", packageMounts)
	}
	if !strings.HasPrefix(packageMounts[0].Source, "stella-selection-") {
		t.Fatalf("package mount source = %q, want selection cache volume", packageMounts[0].Source)
	}
	preparation := session.(*dockerSession).PluginPreparationResult()
	status := make(map[string]pkgplugins.PluginPackageStatus, len(preparation.Packages))
	for _, packageStatus := range preparation.Packages {
		status[packageStatus.PluginID] = packageStatus
	}
	if !status["good"].Ready || status["broken"].Ready {
		t.Fatalf("package readiness = %+v, want good ready and broken failed", status)
	}
	if _, ok := session.Policy().Env["BROKEN_STATIC"]; ok {
		t.Fatalf("failed package static env leaked into session policy: %+v", session.Policy().Env)
	}
	if _, ok := session.Policy().Env["BROKEN_PATH"]; ok {
		t.Fatalf("failed package path env leaked into session policy: %+v", session.Policy().Env)
	}
	if got := session.Policy().Env["BROKEN_SECRET"]; got != "vault-value" {
		t.Fatalf("vault override = %q, want vault-value", got)
	}
	if got := session.Policy().Env["STELLA_HOME"]; got != "/opt/stella" {
		t.Fatalf("runner-owned STELLA_HOME = %q, want /opt/stella", got)
	}
	if got := session.Policy().Env["TMPDIR"]; got != "/tmp" {
		t.Fatalf("runner-owned TMPDIR = %q, want /tmp", got)
	}
	if got := session.Policy().Env["BASH_ENV"]; got != "/workspace/vault-env" {
		t.Fatalf("Vault prior path = %q, want /workspace/vault-env", got)
	}
	if _, ok := session.Policy().Env["BROKEN_OAUTH"]; ok {
		t.Fatalf("failed package OAuth env leaked into session policy: %+v", session.Policy().Env)
	}
	if got := session.Policy().Env["GOOD_SECRET"]; got != "good-value" {
		t.Fatalf("successful package env = %q, want good-value", got)
	}
	containerEnv := make(map[string]string, len(api.createOpts.Config.Env))
	for _, entry := range api.createOpts.Config.Env {
		key, value, found := strings.Cut(entry, "=")
		if found {
			containerEnv[key] = value
		}
	}
	if _, ok := containerEnv["BROKEN_STATIC"]; ok {
		t.Fatalf("failed package static env leaked into container: %v", containerEnv)
	}
	if _, ok := containerEnv["BROKEN_PATH"]; ok {
		t.Fatalf("failed package path env leaked into container: %v", containerEnv)
	}
	if got := containerEnv["BROKEN_SECRET"]; got != "vault-value" {
		t.Fatalf("vault override in container = %q, want vault-value", got)
	}
	if got := containerEnv["STELLA_HOME"]; got != "/opt/stella" {
		t.Fatalf("runner-owned STELLA_HOME in container = %q, want /opt/stella", got)
	}
	if got := containerEnv["TMPDIR"]; got != "/tmp" {
		t.Fatalf("runner-owned TMPDIR in container = %q, want /tmp", got)
	}
	if got := containerEnv["BASH_ENV"]; got != "/workspace/vault-env" {
		t.Fatalf("Vault prior path in container = %q, want /workspace/vault-env", got)
	}
	if _, ok := containerEnv["BROKEN_OAUTH"]; ok {
		t.Fatalf("failed package OAuth env leaked into container: %v", containerEnv)
	}
	if got := containerEnv["GOOD_SECRET"]; got != "good-value" {
		t.Fatalf("successful package env in container = %q, want good-value", got)
	}
}

func TestSelectionMiseTOMLRejectsConflictingScopes(t *testing.T) {
	_, err := selectionMiseTOML([]ToolBinary{
		{PluginID: "tool/uv", ConfigID: "system", Scope: "system", Name: "uv", Tool: "uv", Version: "1"},
		{PluginID: "tool/uv", ConfigID: "user", Scope: "user", Name: "uv", Tool: "uv", Version: "2"},
	})
	if err == nil || !strings.Contains(err.Error(), `disagree on mise tool "uv"`) {
		t.Fatalf("selectionMiseTOML error = %v, want conflicting tool error", err)
	}
}

func TestSelectionToolCacheScriptPublishesCoreAlias(t *testing.T) {
	coreRuntimes := []systemplugins.RuntimeResource{{Name: "xberg", Version: "core-1", Embedded: true}}
	script := selectionToolInstallScript("hash", nil, coreRuntimes)
	if !strings.Contains(script, "test -x \"$ROOT/core/xberg\"") || !strings.Contains(script, "$FINAL_ROOT/core/xberg") {
		t.Fatalf("selection helper did not publish trusted xberg alias:\n%s", script)
	}
	if strings.Contains(script, "ln -s /opt/stella/bin/mise") {
		t.Fatalf("selection helper exposed unselected mise:\n%s", script)
	}
}

func TestSelectionToolInstallScriptCoreOnlyDoesNotExposeMise(t *testing.T) {
	coreRuntimes := []systemplugins.RuntimeResource{
		{Name: "mise", Version: "core-1", Embedded: true},
		{Name: "xberg", Version: "core-1", Embedded: true},
	}
	script := selectionToolInstallScript("hash", nil, coreRuntimes)
	for _, name := range []string{"mise", "xberg"} {
		if !strings.Contains(script, "test -x \"$ROOT/core/"+name+"\"") || !strings.Contains(script, "$FINAL_ROOT/core/"+name) {
			t.Fatalf("core-only selection must publish %s from the image:\n%s", name, script)
		}
	}
	if strings.Contains(script, "/opt/stella/bin/mise install") || strings.Contains(script, "STELLA_SELECTION_MISE_TOML") {
		t.Fatal("core-only selection must not create or run a private mise install")
	}
}

func TestSelectionToolInstallScriptUsesPackageRoot(t *testing.T) {
	root := "/opt/stella/package-tools/package-a"
	script := selectionToolInstallScriptAt(root, "hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}}, nil)
	if !strings.Contains(script, "ROOT='"+root+"'") {
		t.Fatalf("package helper root missing from script:\n%s", script)
	}
	if !strings.Contains(script, "$FINAL_ROOT/artifacts/") || strings.Contains(script, "/opt/stella/selection-tools/artifacts/") {
		t.Fatalf("package helper leaked core selection root:\n%s", script)
	}
	if !strings.Contains(script, ".stella-selection-staging-$$") || !strings.Contains(script, "FINAL_ROOT") {
		t.Fatalf("package helper is not staged before publication:\n%s", script)
	}
}

func TestSelectionToolInstallScriptFailureLeavesNoReadyMarker(t *testing.T) {
	imageRoot := t.TempDir()
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(filepath.Join(imageRoot, "mise"), []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := selectionToolInstallScriptAt(selectionRoot, "hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}}, nil)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageRoot)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	if output, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err == nil {
		t.Fatalf("failed package helper unexpectedly succeeded: %s", output)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, ".stella-selection-ready")); !os.IsNotExist(err) {
		t.Fatalf("failed package helper published ready marker, err=%v", err)
	}
}

func TestSelectionToolInstallScriptPublishesSymlinksOutOfStagingTree(t *testing.T) {
	imageRoot := t.TempDir()
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	privateRoot := filepath.Join(t.TempDir(), "private")
	mise := "#!/bin/sh\nset -eu\ncase \"$1\" in\ntrust) exit 0;;\ninstall) mkdir -p \"$MISE_DATA_DIR/installs/uv/1/bin\"; printf '#!/bin/sh\\n' > \"$MISE_DATA_DIR/installs/uv/1/bin/uv\"; chmod +x \"$MISE_DATA_DIR/installs/uv/1/bin/uv\";;\nwhere) printf '%s' \"$MISE_DATA_DIR/installs/uv/1\";;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(imageRoot, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	script := selectionToolInstallScriptAt(selectionRoot, "hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}}, nil)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageRoot)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	if output, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("package helper failed: %v\n%s", err, output)
	}
	link, err := os.Readlink(filepath.Join(selectionRoot, "bin", "uv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, selectionRoot+"/artifacts/") || strings.Contains(link, "staging") {
		t.Fatalf("published symlink = %q, want final package tree", link)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "bin", "uv")); err != nil {
		t.Fatalf("published symlink is broken: %v", err)
	}
}

func TestSelectionToolInstallScriptRejectsOptionalCoreCollision(t *testing.T) {
	script := selectionToolInstallScript("hash", []ToolBinary{{Name: "xberg", Tool: "github:example/xberg"}}, systemplugins.EmbeddedRuntimeResources())
	if !strings.Contains(script, "selection binary conflicts with mandatory core runtime xberg") {
		t.Fatalf("optional core collision must fail closed:\n%s", script)
	}
}

func TestSelectionToolCacheHashIncludesIdentityAndRevision(t *testing.T) {
	base := []ToolBinary{{PluginID: "tool/one", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "one", Tool: "github:owner/one", Version: "1"}}
	other := []ToolBinary{{PluginID: "tool/one", ConfigID: "cfg", Scope: "system", Revision: 2, Name: "one", Tool: "github:owner/one", Version: "1"}}
	if selectionToolCacheHash("sha256:image-a", base, nil) == selectionToolCacheHash("sha256:image-a", other, nil) {
		t.Fatal("selection cache identity must include config revision")
	}
	packageChanged := append([]ToolBinary(nil), base...)
	packageChanged[0].PackageDigest = "sha256:package-b"
	if selectionToolCacheHash("sha256:image-a", base, nil) == selectionToolCacheHash("sha256:image-a", packageChanged, nil) {
		t.Fatal("selection cache identity must include package digest")
	}
	if selectionToolCacheHash("sha256:image-a", base, nil) == selectionToolCacheHash("sha256:image-b", base, nil) {
		t.Fatal("selection cache identity must include resolved image ID")
	}
	second := ToolBinary{PluginID: "tool/two", ConfigID: "cfg", Scope: "system", Revision: 1, Name: "two", Tool: "uv", Version: "2"}
	if selectionToolCacheHash("sha256:image-a", []ToolBinary{base[0], second}, nil) != selectionToolCacheHash("sha256:image-a", []ToolBinary{second, base[0]}, nil) {
		t.Fatal("selection cache hash must be independent of input order")
	}
	packageA := ToolBinary{PluginID: "same", ConfigID: "cfg", Scope: "system", Revision: 1, PackageDigest: "sha256:a", Name: "same", Tool: "uv", Version: "1"}
	packageB := packageA
	packageB.PackageDigest = "sha256:b"
	if selectionToolCacheHash("sha256:image-a", []ToolBinary{packageA, packageB}, nil) != selectionToolCacheHash("sha256:image-a", []ToolBinary{packageB, packageA}, nil) {
		t.Fatal("selection cache hash must order package identities deterministically")
	}
	coreRuntimes := []systemplugins.RuntimeResource{{Name: "mise", Version: "core-1", Embedded: true}}
	if selectionToolCacheHash("sha256:image-a", nil, coreRuntimes) == selectionToolCacheHash("sha256:image-a", nil, []systemplugins.RuntimeResource{{Name: "mise", Version: "core-2", Embedded: true}}) {
		t.Fatal("selection cache identity must include core runtime revision")
	}
}

func TestToolCachePersistentIDKeepsFullDigest(t *testing.T) {
	hash := "selection-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := toolCachePersistentID(hash); got != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("persistent cache id = %q, want full digest", got)
	}
}

func TestSelectionToolInstallScriptRemovesPrivateInstallerState(t *testing.T) {
	script := selectionToolInstallScript("hash", []ToolBinary{{Name: "uv", Tool: "uv"}}, []systemplugins.RuntimeResource{{Name: "xberg", Embedded: true}})
	for _, required := range []string{"PRIVATE=/tmp/stella-selection-private", "trap 'rm -rf \"$PRIVATE\"'", "cp -R \"$install_dir/.\"", "cp -R /opt/stella/core-runtime/. \"$ROOT/core/\""} {
		if !strings.Contains(script, required) {
			t.Fatalf("selection script missing %q:\n%s", required, script)
		}
	}
	for _, required := range []string{"MISE_CACHE_DIR=\"$PRIVATE/mise-cache\"", "MISE_STATE_DIR=\"$PRIVATE/mise-state\"", "MISE_CONFIG_DIR=\"$PRIVATE/mise-config\"", "MISE_SYSTEM_CONFIG_FILE=\"$PRIVATE/mise.toml\"", "XDG_CACHE_HOME=\"$PRIVATE/xdg-cache\"", "XDG_CONFIG_HOME=\"$PRIVATE/xdg-config\"", "XDG_DATA_HOME=\"$PRIVATE/xdg-data\"", "XDG_STATE_HOME=\"$PRIVATE/xdg-state\""} {
		if !strings.Contains(script, required) {
			t.Fatalf("selection script does not isolate mise path %q:\n%s", required, script)
		}
	}
	if strings.Contains(script, "MISE_SYSTEM_CONFIG_FILE=/opt/stella") || strings.Contains(script, "ln -s /opt/stella/bin/xberg") {
		t.Fatalf("selection script leaks shared installer state or image alias:\n%s", script)
	}
}

func TestSelectionToolInstallScriptPublishesRunnableArtifactsAndNoPrivateState(t *testing.T) {
	imageBin := filepath.Join(t.TempDir(), "image-bin")
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(imageBin, 0o755); err != nil {
		t.Fatal(err)
	}
	mise := `#!/bin/sh
set -eu
case "$1" in
trust) exit 0 ;;
install) mkdir -p "$MISE_DATA_DIR/installs/uv/1/bin"; printf '#!/bin/sh\nprintf "uv-ok\\n"\n' > "$MISE_DATA_DIR/installs/uv/1/bin/uv"; chmod 755 "$MISE_DATA_DIR/installs/uv/1/bin/uv" ;;
where) printf '%s/installs/uv/1\n' "$MISE_DATA_DIR" ;;
esac
`
	if err := os.WriteFile(filepath.Join(imageBin, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(imageBin, "xberg-v1")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "xberg"), []byte("#!/bin/sh\nprintf 'xberg-ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "libxberg.so"), []byte("sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("xberg-v1", "xberg"), filepath.Join(imageBin, "xberg")); err != nil {
		t.Fatal(err)
	}

	script := selectionToolInstallScript("hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}}, []systemplugins.RuntimeResource{{Name: "xberg", Embedded: true}})
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/bin", imageBin)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageBin)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	cmd := exec.Command("/bin/sh", "-c", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("selection installer: %v\n%s\nscript:\n%s", err, output, script)
	}
	for _, private := range []string{"mise.toml", "mise-data"} {
		if _, err := os.Stat(filepath.Join(privateRoot, private)); !os.IsNotExist(err) {
			t.Fatalf("private installer state %s remains, err=%v", private, err)
		}
	}
	identity, err := pkgplugins.BinaryArtifactIdentity(pkgplugins.PluginBinarySpec{Name: "uv", Tool: "uv", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", identity, "bin", "uv")); err != nil {
		t.Fatalf("selected runtime artifact missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "core", "xberg-v1", "libxberg.so")); err != nil {
		t.Fatalf("core sidecar missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "bin", "mise")); !os.IsNotExist(err) {
		t.Fatalf("core mise is absent from this test image, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "bin", "unselected")); !os.IsNotExist(err) {
		t.Fatalf("unselected image binary was published, err=%v", err)
	}
	pathEnv := filepath.Join(selectionRoot, "bin")
	result := exec.Command("/bin/sh", "-c", "uv && xberg")
	result.Env = append(os.Environ(), "PATH="+pathEnv)
	output, err := result.CombinedOutput()
	if err != nil || string(output) != "uv-ok\nxberg-ok\n" {
		t.Fatalf("selected aliases output=%q err=%v", output, err)
	}
}

func TestSelectionToolInstallScriptUsesBuiltinArtifactWithoutMise(t *testing.T) {
	imageRoot := t.TempDir()
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	artifactRoot := filepath.Join(t.TempDir(), "builtin-artifacts")
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(imageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := ToolBinary{Name: "uv", Tool: "uv", Version: "1"}
	identity, err := binaryArtifactIdentity(binary)
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(artifactRoot, identity)
	if err := os.MkdirAll(filepath.Join(artifactDir, "installs", "uv-1", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	uv := filepath.Join(artifactDir, "installs", "uv-1", "uv")
	if err := os.WriteFile(uv, []byte("#!/bin/sh\nprintf 'builtin-uv-ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "installs", "uv-1", "lib", "sidecar.so"), []byte("sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("installs", "uv-1", "uv"), filepath.Join(artifactDir, "uv")); err != nil {
		t.Fatal(err)
	}
	// A different artifact proves the helper only copies the exact fingerprint.
	otherIdentity, err := binaryArtifactIdentity(ToolBinary{Name: "uv", Tool: "uv", Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(artifactRoot, otherIdentity)
	if err := os.MkdirAll(wrong, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrong, "other"), []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}

	miseMarker := filepath.Join(t.TempDir(), "mise-called")
	mise := "#!/bin/sh\nprintf called > \"$MISE_MARKER\"\nexit 99\n"
	if err := os.WriteFile(filepath.Join(imageRoot, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	binary.PluginID = "uv"
	shared := binary
	shared.PluginID = "other-package"
	script := selectionToolInstallScript("hash", []ToolBinary{binary, shared}, nil)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/.mise-tools/builtin-artifacts", artifactRoot)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageRoot)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "MISE_MARKER="+miseMarker)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline builtin artifact selection: %v\n%s\nscript:\n%s", err, output, script)
	}
	if _, err := os.Stat(miseMarker); !os.IsNotExist(err) {
		t.Fatalf("builtin artifact hit invoked mise, err=%v", err)
	}
	selected := filepath.Join(selectionRoot, "bin", "uv")
	result := exec.Command(selected)
	output, err := result.CombinedOutput()
	if err != nil || string(output) != "builtin-uv-ok\n" {
		t.Fatalf("selected builtin alias output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", identity, "installs", "uv-1", "lib", "sidecar.so")); err != nil {
		t.Fatalf("builtin artifact sidecar missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", otherIdentity)); !os.IsNotExist(err) {
		t.Fatalf("unselected artifact became reachable, err=%v", err)
	}
}

func TestSelectionToolInstallScriptRejectsBuiltinArtifactMismatch(t *testing.T) {
	imageRoot := t.TempDir()
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	artifactRoot := filepath.Join(t.TempDir(), "builtin-artifacts")
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(imageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := ToolBinary{Name: "uv", Tool: "uv", Version: "1"}
	otherIdentity, err := binaryArtifactIdentity(ToolBinary{Name: "uv", Tool: "uv", Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(artifactRoot, otherIdentity)
	if err := os.MkdirAll(wrong, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrong, "uv"), []byte("#!/bin/sh\nprintf wrong\\n\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mise := `#!/bin/sh
set -eu
case "$1" in
trust) exit 0 ;;
install)
  mkdir -p "$MISE_DATA_DIR/installs/uv/1/bin"
  cat > "$MISE_DATA_DIR/installs/uv/1/bin/uv" <<'EOF'
#!/bin/sh
printf 'mise-uv-ok\n'
EOF
  chmod 755 "$MISE_DATA_DIR/installs/uv/1/bin/uv"
  ;;
where) printf '%s/installs/uv/1\n' "$MISE_DATA_DIR" ;;
esac
`
	if err := os.WriteFile(filepath.Join(imageRoot, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	script := selectionToolInstallScript("hash", []ToolBinary{binary}, nil)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/.mise-tools/builtin-artifacts", artifactRoot)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageRoot)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	cmd := exec.Command("/bin/sh", "-c", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mismatched builtin artifact fallback: %v\n%s\nscript:\n%s", err, output, script)
	}
	result := exec.Command(filepath.Join(selectionRoot, "bin", "uv"))
	output, err := result.CombinedOutput()
	if err != nil || string(output) != "mise-uv-ok\n" {
		t.Fatalf("isolated fallback output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(selectionRoot, "artifacts", otherIdentity)); !os.IsNotExist(err) {
		t.Fatalf("mismatched artifact was copied, err=%v", err)
	}
}

func TestSelectionToolInstallScriptMixedHitInstallsOnlyMiss(t *testing.T) {
	imageRoot := t.TempDir()
	selectionRoot := filepath.Join(t.TempDir(), "selection")
	artifactRoot := filepath.Join(t.TempDir(), "builtin-artifacts")
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(imageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	hit := ToolBinary{Name: "uv", Tool: "uv", Version: "1"}
	miss := ToolBinary{Name: "bun", Tool: "bun", Version: "1"}
	hitIdentity, err := binaryArtifactIdentity(hit)
	if err != nil {
		t.Fatal(err)
	}
	hitDir := filepath.Join(artifactRoot, hitIdentity)
	if err := os.MkdirAll(filepath.Join(hitDir, "installs", "uv-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hitDir, "installs", "uv-1", "uv"), []byte("#!/bin/sh\nprintf 'hit-uv\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("installs", "uv-1", "uv"), filepath.Join(hitDir, "uv")); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "mise.log")
	mise := `#!/bin/sh
set -eu
printf '%s %s\n' "$1" "${2-}" >> "$MISE_LOG"
case "$1" in
trust) exit 0 ;;
install) mkdir -p "$MISE_DATA_DIR/installs/bun/1/bin"; cat > "$MISE_DATA_DIR/installs/bun/1/bin/bun" <<'EOF2'
#!/bin/sh
printf 'miss-bun\n'
EOF2
chmod 755 "$MISE_DATA_DIR/installs/bun/1/bin/bun" ;;
where)
  case "$2" in
  bun) printf '%s/installs/bun/1\n' "$MISE_DATA_DIR" ;;
  uv) printf '%s/installs/uv/1\n' "$MISE_DATA_DIR" ;;
  esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(imageRoot, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	script := selectionToolInstallScript("hash", []ToolBinary{hit, miss}, nil)
	script = strings.ReplaceAll(script, "/opt/stella/selection-tools", selectionRoot)
	script = strings.ReplaceAll(script, "/opt/stella/.mise-tools/builtin-artifacts", artifactRoot)
	script = strings.ReplaceAll(script, "/opt/stella/core-runtime", imageRoot)
	script = strings.ReplaceAll(script, "/tmp/stella-selection-private", privateRoot)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "MISE_LOG="+logPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mixed artifact selection: %v\n%s\nscript:\n%s", err, output, script)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	if strings.Contains(log, "where uv") {
		t.Fatalf("artifact hit invoked mise where: %s", log)
	}
	if !strings.Contains(log, "install ") || !strings.Contains(log, "where bun") {
		t.Fatalf("artifact miss did not run isolated install and lookup: %s", log)
	}
	for _, want := range []struct {
		name string
		text string
	}{
		{"uv", "hit-uv\n"},
		{"bun", "miss-bun\n"},
	} {
		result := exec.Command(filepath.Join(selectionRoot, "bin", want.name))
		output, runErr := result.CombinedOutput()
		if runErr != nil || string(output) != want.text {
			t.Fatalf("selected %s output=%q err=%v", want.name, output, runErr)
		}
	}
}

func TestSelectionToolInstallScriptRejectsConflictingCommandBeforePublication(t *testing.T) {
	for name, conflict := range map[string]ToolBinary{
		"source":  {Name: "uv", Tool: "github:other/uv", Version: "1"},
		"version": {Name: "uv", Tool: "uv", Version: "2"},
		"options": {Name: "uv", Tool: "uv", Version: "1", Options: map[string]any{"asset_pattern": "other"}},
	} {
		t.Run(name, func(t *testing.T) {
			script := selectionToolInstallScript("hash", []ToolBinary{{Name: "uv", Tool: "uv", Version: "1"}, conflict}, nil)
			output, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", script).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "selected binaries disagree on command uv") {
				t.Fatalf("conflicting command result = %q, %v", output, err)
			}
			if strings.Contains(script, "mkdir") || strings.Contains(script, "rm -rf") {
				t.Fatal("conflicting command must be rejected before modifying selection storage")
			}
		})
	}
}
