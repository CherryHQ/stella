package toolinstall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRenderTOMLUsesToolOptionsAndRejectsShimCollisions(t *testing.T) {
	got, err := RenderTOML([]Tool{{Key: "github:cli/cli", Version: "2.0.0", Lookup: "gh", Options: map[string]any{"bin_path": "bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[tools.'github:cli/cli']", "version = '2.0.0'", "bin_path = 'bin'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderTOML missing %q in %s", want, got)
		}
	}
	if _, err := RenderTOML([]Tool{{Key: "a", Lookup: "tool"}, {Key: "b", Lookup: "tool"}}); err == nil {
		t.Fatal("RenderTOML accepted conflicting shim names")
	}
}

func TestWarmIsolatesHostEnvAndLeavesNoSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise uses POSIX shell")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "mise.log")
	fake := filepath.Join(binDir, "mise")
	script := `#!/bin/sh
set -eu
printf 'data=%s config=%s project=%s home=%s\n' "$MISE_DATA_DIR" "$MISE_GLOBAL_CONFIG_FILE" "$MISE_PROJECT_ROOT" "$HOME" >> ` + shellQuotePOSIX(logPath) + `
case "$1" in
  trust) test -f "$2" ;;
  install) grep -q "github:owner/repo" "$MISE_GLOBAL_CONFIG_FILE" ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISE_PROJECT_ROOT", "/ambient/project")
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "/ambient/config")
	if err := Warm(t.Context(), home, []Tool{{Key: "github:owner/repo", Version: "1.2.3", Lookup: "tool", PublicName: "tool"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Contains(log, "/ambient") || !strings.Contains(log, "data="+filepath.Join(home, ".mise-tools")) {
		t.Fatalf("ambient mise environment leaked: %s", log)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "tool")); !os.IsNotExist(err) {
		t.Fatalf("Warm published a selection, stat err=%v", err)
	}
}

func TestPublishNativeSelectionIsAtomicAndReusable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	var builds int
	build := func(dir string) error {
		builds++
		return os.WriteFile(filepath.Join(dir, "tool"), []byte("selected"), 0o755)
	}
	if err := publishNativeSelection(root, []string{"tool"}, build); err != nil {
		t.Fatal(err)
	}
	if err := publishNativeSelection(root, []string{"tool"}, func(dir string) error {
		builds++
		return os.WriteFile(filepath.Join(dir, "tool"), []byte("replaced"), 0o755)
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tool"))
	if err != nil || string(data) != "selected" || builds != 1 {
		t.Fatalf("publication rebuilt: builds=%d data=%q err=%v", builds, data, err)
	}

	root = filepath.Join(t.TempDir(), "concurrent")
	builds = 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := publishNativeSelection(root, []string{"tool"}, build); err != nil {
				t.Errorf("publish: %v", err)
			}
		})
	}
	wg.Wait()
	if builds != 1 {
		t.Fatalf("concurrent publication built %d times", builds)
	}
}

func TestCopyNativeTreeRejectsEscapingAndCyclicSymlinks(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyNativeTree(source, filepath.Join(t.TempDir(), "copy")); err == nil || !strings.Contains(err.Error(), "absolute or unsafe target") {
		t.Fatalf("escaping symlink error = %v", err)
	}

	cycle := t.TempDir()
	if err := os.Symlink("b", filepath.Join(cycle, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(cycle, "b")); err != nil {
		t.Fatal(err)
	}
	if err := copyNativeTree(cycle, filepath.Join(t.TempDir(), "copy")); err == nil || !strings.Contains(err.Error(), "symlink cycle") {
		t.Fatalf("cyclic symlink error = %v", err)
	}
}

func TestMaterializeNativeSelectionCanonicalizesMisePathsAndCopiesSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise uses POSIX shell")
	}
	root := t.TempDir()
	install := filepath.Join(root, "install")
	binary := filepath.Join(install, "bin", "bun")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("bun-real\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "runtime.dat"), []byte("sidecar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installAlias := filepath.Join(root, "install-alias")
	if err := os.Symlink(install, installAlias); err != nil {
		t.Fatal(err)
	}
	mise := filepath.Join(root, "mise")
	script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nwhere) printf '%s\\n' " + shellQuotePOSIX(installAlias) + ";;\nwhich) printf '%s\\n' " + shellQuotePOSIX(binary) + ";;\n*) exit 9;;\nesac\n"
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "public")
	tool := Tool{Key: "github:oven/bun", Lookup: "bun", PublicName: "bun"}
	plan := Selection{PublicDir: public, PublicBinDir: public}
	if err := materializeNativeSelection(t.Context(), filepath.Join(root, "stella"), plan, []Tool{tool}, mise, nil, root); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(public, "installs", nativeInstallKey(tool))
	if data, err := os.ReadFile(filepath.Join(selected, "bin", "bun")); err != nil || string(data) != "bun-real\n" {
		t.Fatalf("selected binary = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(selected, "runtime.dat")); err != nil || string(data) != "sidecar\n" {
		t.Fatalf("selected sidecar = %q, err=%v", data, err)
	}
}

func TestNativeMiseInstallEnvIgnoresAmbientConfig(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	private := filepath.Join(home, "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "/ambient/home")
	t.Setenv("MISE_CONFIG_DIR", "/ambient/config")
	t.Setenv("MISE_SYSTEM_CONFIG_FILE", "/ambient/system.toml")
	env, err := nativeMiseInstallEnv(home, data, filepath.Join(private, "shims"), private, filepath.Join(private, "selection.toml"), filepath.Join(private, "system.toml"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, ambient := range []string{"/ambient/home", "/ambient/config", "/ambient/system.toml"} {
		if strings.Contains(joined, ambient) {
			t.Fatalf("ambient mise setting leaked into isolated env: %q", ambient)
		}
	}
	for _, expected := range []string{"MISE_DATA_DIR=" + data, "MISE_GLOBAL_CONFIG_FILE=" + filepath.Join(private, "selection.toml"), "MISE_SYSTEM_CONFIG_FILE=" + filepath.Join(private, "system.toml")} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("isolated env missing %q: %s", expected, joined)
		}
	}
}

func TestNativeMiseErrorsHideStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise uses POSIX shell")
	}
	mise := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(mise, []byte("#!/bin/sh\necho secret-install-output >&2\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runMise(t.Context(), mise, nil, t.TempDir(), "install")
	if err == nil || strings.Contains(err.Error(), "secret-install-output") || len(err.Error()) > 128 {
		t.Fatalf("runMise error = %v", err)
	}
	_, err = runMiseOutput(t.Context(), mise, nil, t.TempDir(), "where", "bun")
	if err == nil || strings.Contains(err.Error(), "secret-install-output") || len(err.Error()) > 128 {
		t.Fatalf("runMiseOutput error = %v", err)
	}
}

func TestRemoveNativeMiseConfigReportsCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeNativeMiseConfig(dir); err == nil {
		t.Fatal("removing a non-empty config path must report cleanup failure")
	}
}

func TestPublishNativeSelectionReusesCompleteIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	builds := 0
	build := func(dir string) error {
		builds++
		return os.WriteFile(filepath.Join(dir, "tool"), []byte("old"), 0o755)
	}
	if err := publishNativeSelection(root, []string{"tool"}, build); err != nil {
		t.Fatalf("first publication: %v", err)
	}
	before, err := os.Stat(filepath.Join(root, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if err := publishNativeSelection(root, []string{"tool"}, func(dir string) error {
		builds++
		return os.WriteFile(filepath.Join(dir, "tool"), []byte("new"), 0o755)
	}); err != nil {
		t.Fatalf("repeat publication: %v", err)
	}
	after, err := os.Stat(filepath.Join(root, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 || !os.SameFile(before, after) || string(data) != "old" {
		t.Fatalf("complete publication was rebuilt: builds=%d sameFile=%v content=%q", builds, os.SameFile(before, after), data)
	}
}

func TestPublishNativeSelectionSerializesConcurrentIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "selection")
	var builds int
	build := func(dir string) error {
		builds++
		return os.WriteFile(filepath.Join(dir, "tool"), []byte("selected"), 0o755)
	}
	const callers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			errCh <- publishNativeSelection(root, []string{"tool"}, build)
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent publication: %v", err)
		}
	}
	if builds != 1 {
		t.Fatalf("concurrent publication built %d times, want one", builds)
	}
	if data, err := os.ReadFile(filepath.Join(root, "tool")); err != nil || string(data) != "selected" {
		t.Fatalf("published selection = %q, err=%v", data, err)
	}
}

func TestCopyNativeTreeRejectsEscapingSymlink(t *testing.T) {
	source := t.TempDir()
	nested := filepath.Join(source, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Dir(filepath.Join(nested, "escape")), secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(nested, "escape")); err != nil {
		t.Fatal(err)
	}

	err = copyNativeTree(source, filepath.Join(t.TempDir(), "copy"))
	if err == nil || !strings.Contains(err.Error(), "escapes install") {
		t.Fatalf("copyNativeTree error = %v, want escaping symlink rejection", err)
	}
}

func TestCopyNativeTreePortableAbsoluteSymlinkAndChain(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "installs", "uv", "bin", "uv")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("uv-ready"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(source, ".mise-bins")
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}
	chain := filepath.Join(links, "uv-chain")
	if err := os.Symlink(target, chain); err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonicalTarget, filepath.Join(links, "uv-canonical")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(chain, filepath.Join(links, "uv")); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "copy")
	if err := copyNativeTree(source, destination); err != nil {
		t.Fatalf("copyNativeTree: %v", err)
	}
	for _, name := range []string{"uv", "uv-chain", "uv-canonical"} {
		link := filepath.Join(destination, ".mise-bins", name)
		gotTarget, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("read copied %s link: %v", name, err)
		}
		if filepath.IsAbs(gotTarget) {
			t.Fatalf("copied %s link retained absolute target %q", name, gotTarget)
		}
		got, err := os.ReadFile(link)
		if err != nil || string(got) != "uv-ready" {
			t.Fatalf("read copied %s target = %q, err=%v", name, got, err)
		}
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, ".mise-bins", "uv")); err != nil || string(got) != "uv-ready" {
		t.Fatalf("copied selection depends on source after publication: %q, err=%v", got, err)
	}
}

func TestCopyNativeTreeRejectsAbsoluteSiblingAndSymlinkCycle(t *testing.T) {
	root := t.TempDir()
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(sibling, "secret")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "absolute-escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyNativeTree(root, filepath.Join(t.TempDir(), "absolute-copy")); err == nil || !strings.Contains(err.Error(), "absolute or unsafe target") {
		t.Fatalf("absolute sibling link error = %v, want rejection", err)
	}

	cycleRoot := t.TempDir()
	if err := os.Symlink("b", filepath.Join(cycleRoot, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(cycleRoot, "b")); err != nil {
		t.Fatal(err)
	}
	if err := copyNativeTree(cycleRoot, filepath.Join(t.TempDir(), "cycle-copy")); err == nil || !strings.Contains(err.Error(), "symlink cycle") {
		t.Fatalf("symlink cycle error = %v, want rejection", err)
	}
}

func TestMaterializeNativeSelectionCanonicalizesMisePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	root := t.TempDir()
	install := filepath.Join(root, "install")
	binary := filepath.Join(install, "bin", "bun")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("bun-real\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "runtime.dat"), []byte("sidecar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installAlias := filepath.Join(root, "install-alias")
	if err := os.Symlink(install, installAlias); err != nil {
		t.Fatal(err)
	}
	mise := filepath.Join(root, "mise")
	script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nwhere) printf '%s\\n' " + shellQuotePOSIX(installAlias) + ";;\nwhich) printf '%s\\n' " + shellQuotePOSIX(binary) + ";;\n*) exit 9;;\nesac\n"
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "public")
	tool := Tool{Key: "github:oven/bun", Lookup: "bun", PublicName: "bun"}
	selection := Selection{PublicDir: public, PublicBinDir: public}
	if err := materializeNativeSelection(context.Background(), filepath.Join(root, "stella"), selection, []Tool{tool}, mise, nil, root); err != nil {
		t.Fatalf("materializeNativeSelection: %v", err)
	}
	selected := filepath.Join(public, "installs", nativeInstallKey(tool))
	if data, err := os.ReadFile(filepath.Join(selected, "bin", "bun")); err != nil || string(data) != "bun-real\n" {
		t.Fatalf("selected bun = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(selected, "runtime.dat")); err != nil || string(data) != "sidecar\n" {
		t.Fatalf("selected sidecar = %q, err=%v", data, err)
	}
}

func TestInstallNativeMiseSelectionReturnsBeforeMiseWhenPublicationIsComplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(stellaHome, "mise-called")
	fake := "#!/bin/sh\nprintf called > " + shellQuotePOSIX(called) + "\nexit 99\n"
	if err := os.WriteFile(filepath.Join(binDir, "mise"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(stellaHome, ".mise-tools", "public", "core-ready")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mise", "fd"} {
		if err := os.WriteFile(filepath.Join(public, name), []byte(name+"-ready\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := InstallSelection(context.Background(), stellaHome, Selection{
		DataDir:       filepath.Join(stellaHome, ".mise-tools"),
		PublicDir:     public,
		PublicBinDir:  public,
		EmbeddedNames: []string{"mise"},
	}, []Tool{{Key: "github:sharkdp/fd", Lookup: "fd", PublicName: "fd", Version: "10.4.2"}})
	if err != nil {
		t.Fatalf("InstallSelection: %v", err)
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("complete publication invoked mise, stat err=%v", err)
	}
}

func TestNativeMiseInstallEnvIgnoresAmbientToolVersions(t *testing.T) {
	miseBin, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("real mise is not installed")
	}
	stellaHome := t.TempDir()
	dataDir := filepath.Join(stellaHome, ".mise-tools")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stellaHome, ".tool-versions"), []byte("rust 1.80.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stellaHome, "mise.toml"), []byte("[tools]\ntailspin = '0.1.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostHome := t.TempDir()
	hostXDG := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(hostXDG, "mise"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(hostHome, ".config", "mise", "config.toml"),
		filepath.Join(hostXDG, "mise", "config.toml"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[tools]\nzellij = '0.1.0'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hostSystem := filepath.Join(t.TempDir(), "system.toml")
	if err := os.WriteFile(hostSystem, []byte("[tools]\nherd = '0.1.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", hostXDG)
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", filepath.Join(hostHome, ".config", "mise", "config.toml"))
	t.Setenv("MISE_SYSTEM_CONFIG_FILE", hostSystem)
	private := filepath.Join(stellaHome, ".mise-private", "install-ambient")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(private, "selection.toml")
	systemConfig := filepath.Join(private, "system.toml")
	if err := os.WriteFile(globalConfig, []byte("[tools]\nbun = '1.3.14'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := nativeMiseInstallEnv(stellaHome, dataDir, filepath.Join(private, "shims"), private, globalConfig, systemConfig)
	if err != nil {
		t.Fatalf("nativeMiseInstallEnv: %v", err)
	}
	output, err := runMiseOutput(context.Background(), miseBin, env, private, "ls", "--json")
	if err != nil {
		t.Fatalf("real mise ls: %v", err)
	}
	for _, ambient := range []string{"rust", "tailspin", "zellij", "herd"} {
		if strings.Contains(strings.ToLower(output), ambient) {
			t.Fatalf("ambient %s config leaked into native selection: %q", ambient, output)
		}
	}
	if !strings.Contains(strings.ToLower(output), "bun") {
		t.Fatalf("selection config was not used by real mise: %q", output)
	}
}

func TestSandboxMiseInstallCommandUsesExplicitEnginePath(t *testing.T) {
	command := sandboxMiseInstallCommand()
	if strings.Contains(command, "&& mise ") || strings.HasPrefix(command, "mise ") {
		t.Fatalf("sandbox installer must invoke the runner-owned mise executable explicitly: %q", command)
	}
	if !strings.Contains(command, "STELLA_HOME") {
		t.Fatalf("sandbox installer command must resolve mise from STELLA_HOME: %q", command)
	}
}

func TestRenderMiseTOMLDeduplicatesIdenticalMiseToolSelections(t *testing.T) {
	tool := Tool{Key: "bun", Version: "1.3.14", Lookup: "bun", PublicName: "bun"}
	got, err := RenderTOML([]Tool{tool, tool})
	if err != nil {
		t.Fatalf("RenderTOML: %v", err)
	}
	if strings.Count(got, "bun =") != 1 {
		t.Fatalf("duplicate bun declaration was not collapsed: %q", got)
	}
}
