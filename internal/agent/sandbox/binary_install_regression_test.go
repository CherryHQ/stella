package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	nonebackend "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func TestContextBinaryInstallPlanIsolatedBySelectionIdentity(t *testing.T) {
	stellaHome := t.TempDir()
	base := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "one", ConfigID: "cfg-one", Scope: string(plugin.ScopeSystem), Revision: 4,
		},
		Name: "one", Tool: "github:owner/one", Version: "1.0.0",
	}
	other := base
	other.ConfigID = "cfg-two"
	other.Scope = string(plugin.ScopeSystemAgent)
	other.Revision = 7
	other.Version = "2.0.0"

	first, err := testContextSelectionPlan(stellaHome, []pkgplugins.PluginBinarySpec{base})
	if err != nil {
		t.Fatalf("testContextSelectionPlan(first): %v", err)
	}
	second, err := testContextSelectionPlan(stellaHome, []pkgplugins.PluginBinarySpec{other})
	if err != nil {
		t.Fatalf("testContextSelectionPlan(second): %v", err)
	}
	if first.Identity == second.Identity || first.PublicDir == second.PublicDir || first.PublicBinDir == second.PublicBinDir {
		t.Fatalf("selection identities must isolate public selections: first=%+v second=%+v", first, second)
	}
	if !strings.HasPrefix(first.PublicDir, filepath.Join(stellaHome, ".mise-tools", "public")) {
		t.Fatalf("public selection escaped managed public root: %q", first.PublicDir)
	}
}

func TestContextBinaryInstallPlanRejectsPathTraversal(t *testing.T) {
	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "evil", ConfigID: "cfg", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
		Name: "../../victim", Tool: "github:owner/tool",
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testContextSelectionPlan(filepath.Dir(victim), []pkgplugins.PluginBinarySpec{spec}); err == nil {
		t.Fatal("path traversal name must be rejected before publication")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("path traversal touched external victim: data=%q err=%v", data, err)
	}
}

func TestContextBinaryInstallPlanRejectsNonPositiveRevision(t *testing.T) {
	base := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "one", ConfigID: "cfg-one", Scope: string(plugin.ScopeSystem),
		},
		Name: "one", Tool: "github:owner/one", Version: "1.0.0",
	}
	for _, revision := range []int64{0, -1} {
		t.Run(fmt.Sprintf("revision_%d", revision), func(t *testing.T) {
			spec := base
			spec.Revision = revision
			if _, err := testContextSelectionPlan(t.TempDir(), []pkgplugins.PluginBinarySpec{spec}); err == nil || !strings.Contains(err.Error(), "non-positive config revision") {
				t.Fatalf("testContextSelectionPlan() error = %v, want non-positive revision", err)
			}
		})
	}
}

func TestOverlayNativeSelectionDropsSharedMisePaths(t *testing.T) {
	plan := BinaryInstallPlan{Selections: []BinarySelectionPlan{{PublicDir: "/opt/stella/.mise-tools/public/selection", PublicBinDir: "/opt/stella/.mise-tools/public/selection"}}}
	base := map[string]string{
		"PATH":                      "/usr/bin",
		"MISE_DATA_DIR":             "/opt/stella/.mise-tools",
		"MISE_CONFIG_DIR":           "/opt/stella/.mise-tools/config",
		"MISE_CACHE_DIR":            "/opt/stella/.mise-tools/cache",
		"MISE_STATE_DIR":            "/opt/stella/.mise-tools/state",
		"MISE_GLOBAL_CONFIG_FILE":   "/opt/stella/.mise-tools/config.toml",
		"MISE_TRUSTED_CONFIG_PATHS": "/opt/stella/.mise-tools/config.toml",
	}
	env := OverlayBinaryInstallPlan(base, plan, BinarySystemLayer)
	for _, key := range []string{"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_GLOBAL_CONFIG_FILE", "MISE_TRUSTED_CONFIG_PATHS"} {
		if _, ok := env[key]; ok {
			t.Fatalf("native selection retained shared mise path %s=%q", key, env[key])
		}
	}
	if !strings.HasPrefix(env["PATH"], plan.Selections[0].PublicBinDir+string(filepath.ListSeparator)) {
		t.Fatalf("native selection PATH = %q, want public selection first", env["PATH"])
	}
	if env[pkgsandbox.EnvNativeSelectionDir] != plan.Selections[0].PublicBinDir {
		t.Fatalf("native selection marker = %q, want %q", env[pkgsandbox.EnvNativeSelectionDir], plan.Selections[0].PublicBinDir)
	}
}

func TestOverlayNativeSelectionKeepsPrivateUserMiseTree(t *testing.T) {
	plan := BinaryInstallPlan{Selections: []BinarySelectionPlan{{PublicDir: "/opt/stella/.mise-tools/public/selection", PublicBinDir: "/opt/stella/.mise-tools/public/selection"}}}
	base := map[string]string{
		"PATH":                        "/usr/bin",
		"MISE_DATA_DIR":               "/opt/stella/users/u/.mise-tools",
		"MISE_CONFIG_DIR":             "/opt/stella/users/u/.mise-tools/config",
		"MISE_CACHE_DIR":              "/opt/stella/users/u/.mise-tools/cache",
		"MISE_STATE_DIR":              "/opt/stella/users/u/.mise-tools/state",
		"MISE_GLOBAL_CONFIG_FILE":     "/opt/stella/users/u/.mise-tools/config.toml",
		"MISE_NOT_FOUND_AUTO_INSTALL": "true",
	}
	env := OverlayBinaryInstallPlan(base, plan, BinarySystemLayer)
	for _, key := range []string{"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_GLOBAL_CONFIG_FILE"} {
		if env[key] != base[key] {
			t.Fatalf("native system overlay changed private user path %s=%q", key, env[key])
		}
	}
}

func TestContextBinaryInstallRejectsConflictingMiseToolSelections(t *testing.T) {
	baseIdentity := pkgplugins.PluginResourceIdentity{PluginID: "one", ConfigID: "cfg-one", Scope: string(plugin.ScopeSystem), Revision: 1}
	specs := []pkgplugins.PluginBinarySpec{
		{PluginResourceIdentity: baseIdentity, Name: "one", Tool: "github:owner/shared", Version: "1.0.0"},
		{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "two", ConfigID: "cfg-two", Scope: string(plugin.ScopeSystemAgent), Revision: 1}, Name: "two", Tool: "github:owner/shared", Version: "2.0.0"},
	}
	if _, err := testContextSelectionPlan(t.TempDir(), specs); err != nil {
		t.Fatalf("identity should accept distinct resources: %v", err)
	}
	if _, err := InstallContextBinaries(t.Context(), t.TempDir(), specs); err == nil || !strings.Contains(err.Error(), "disagree on mise tool") {
		t.Fatalf("conflicting mise selections should fail closed, got %v", err)
	}
}

func TestInstallContextBinariesUsesSelectionLocalConfigAndShims(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(stellaHome, "mise.log")
	fake := "#!/bin/sh\nset -eu\nprintf '%s|data=%s|config=%s|shims=%s\\n' \"$1\" \"${MISE_DATA_DIR-}\" \"${MISE_GLOBAL_CONFIG_FILE-}\" \"${MISE_SHIMS_DIR-}\" >> " + shellQuote(logPath) + "\n"
	fake += "case \"$1\" in\n"
	fake += "trust) exit 0 ;;\n"
	fake += "install) mkdir -p \"$MISE_DATA_DIR/installs/test-one/1.0.0/bin\"; printf '#!/bin/sh\\necho one\\n' > \"$MISE_DATA_DIR/installs/test-one/1.0.0/bin/one\"; chmod 755 \"$MISE_DATA_DIR/installs/test-one/1.0.0/bin/one\"; printf sidecar > \"$MISE_DATA_DIR/installs/test-one/1.0.0/runtime.dat\"; exit 0 ;;\n"
	fake += "where) printf '%s\\n' \"$MISE_DATA_DIR/installs/test-one/1.0.0\" ;;\n"
	fake += "which) printf '%s\\n' \"$MISE_DATA_DIR/installs/test-one/1.0.0/bin/one\" ;;\n"
	fake += "*) exit 9 ;;\nesac\n"
	fakePath := filepath.Join(binDir, "mise")
	if err := os.WriteFile(fakePath, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "one", ConfigID: "cfg-one", Scope: string(plugin.ScopeSystem), Revision: 4,
		},
		Name: "one", Tool: "github:owner/one", Version: "1.0.0", Options: map[string]any{"private": "secret"},
	}
	result, err := InstallContextBinaries(t.Context(), stellaHome, []pkgplugins.PluginBinarySpec{spec})
	if err != nil {
		t.Fatalf("InstallContextBinaries: %v", err)
	}
	plan := result.Plan.Selections[0]
	if _, err := os.Stat(filepath.Join(stellaHome, ".mise-tools", "_builtin.toml")); !os.IsNotExist(err) {
		t.Fatalf("context installer must not rewrite _builtin.toml, stat err=%v", err)
	}
	if plan.PublicBinDir == "" {
		t.Fatalf("native plan missed public bin: %+v", plan)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "config=") || strings.Contains(log, "config="+plan.PublicDir) || strings.Contains(log, "config="+plan.DataDir) {
		t.Fatalf("installer did not use selection-local paths, plan=%+v log=%s", plan, log)
	}
	if strings.Count(log, "data="+plan.DataDir) != 4 {
		t.Fatalf("installer did not use shared artifact data dir for all steps, log=%s", log)
	}
	if strings.Contains(string(logBytes), "secret") {
		t.Fatalf("native private options leaked into installer log: %s", log)
	}
	privateInfo, err := os.Stat(filepath.Join(stellaHome, ".mise-private"))
	if err != nil {
		t.Fatalf("native private root missing: %v", err)
	}
	if privateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("native private root mode = %o, want 700", privateInfo.Mode().Perm())
	}
	if entries, err := os.ReadDir(filepath.Join(stellaHome, ".mise-private")); err != nil || len(entries) != 0 {
		t.Fatalf("native private root retained installer state: entries=%v err=%v", entries, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(plan.PublicDir, "installs", "*", "runtime.dat")); len(matches) != 1 {
		t.Fatalf("selected install sidecar missing: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(plan.PublicBinDir, "one")); err != nil {
		t.Fatalf("selected direct alias missing: %v", err)
	}
	output, err := exec.Command(filepath.Join(plan.PublicBinDir, "one")).Output()
	if err != nil || string(output) != "one\n" {
		t.Fatalf("selected alias output = %q, err=%v; want selected version", output, err)
	}
	var leaked bool
	_ = filepath.WalkDir(filepath.Join(stellaHome, ".mise-tools"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte("secret")) {
			leaked = true
		}
		return readErr
	})
	if leaked {
		t.Fatal("native private options persisted in the shared mise tree")
	}
}

func TestInstallContextBinariesResultKeepsSuccessfulPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stellaHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
set -eu
case "$1" in
trust|reshim) exit 0 ;;
install)
  mkdir -p "$MISE_DATA_DIR/installs/good/1/bin"
  printf '#!/bin/sh\necho good\n' > "$MISE_DATA_DIR/installs/good/1/bin/good"
  chmod 755 "$MISE_DATA_DIR/installs/good/1/bin/good"
  exit 0 ;;
where)
  case "$2" in *bad*) exit 17 ;; esac
  printf '%s\n' "$MISE_DATA_DIR/installs/good/1"
  exit 0 ;;
which)
  case "$2" in *bad*) exit 17 ;; esac
  printf '%s\n' "$MISE_DATA_DIR/installs/good/1/bin/good"
  exit 0 ;;
*) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(stellaHome, "bin", "mise"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	good := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "good", ConfigID: "cfg-good", Scope: string(plugin.ScopeSystem), Revision: 1},
		Name:                   "good", Tool: "github:owner/good", Version: "1.0.0",
	}
	bad := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "bad", ConfigID: "cfg-bad", Scope: string(plugin.ScopeSystem), Revision: 1},
		Name:                   "bad", Tool: "github:owner/bad", Version: "1.0.0",
	}
	result, err := InstallContextBinaries(t.Context(), stellaHome, []pkgplugins.PluginBinarySpec{bad, good})
	if err != nil {
		t.Fatalf("InstallContextBinariesResult: %v", err)
	}
	if len(result.SuccessfulPackages) != 1 || result.SuccessfulPackages[0].PluginID != "good" {
		t.Fatalf("successful packages = %+v, want good only", result.SuccessfulPackages)
	}
	if len(result.FailedPackages) != 1 || result.FailedPackages[0].Package.PluginID != "bad" {
		t.Fatalf("failed packages = %+v, want bad only", result.FailedPackages)
	}
	if len(result.Plan.Selections) != 1 || result.Plan.Selections[0].Package.PluginID != "good" {
		t.Fatalf("published selections = %+v, want good only", result.Plan.Selections)
	}
	if _, err := os.Stat(filepath.Join(result.Plan.Selections[0].PublicBinDir, "good")); err != nil {
		t.Fatalf("successful package was not published: %v", err)
	}
	env := OverlayBinaryInstallPlan(map[string]string{"PATH": "/usr/bin"}, result.Plan, BinarySystemLayer)
	if !strings.HasPrefix(env["PATH"], result.Plan.Selections[0].PublicBinDir+string(filepath.ListSeparator)) {
		t.Fatalf("successful package selection missing from PATH: %q", env["PATH"])
	}
	for _, selection := range result.Plan.Selections {
		if selection.Package.PluginID == "bad" {
			t.Fatalf("failed package has a published selection: %+v", selection)
		}
	}
}

func TestInstallContextBinariesSkipsEmptySystemSelection(t *testing.T) {
	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "one", ConfigID: "cfg-one", Scope: string(plugin.ScopeUser), Revision: 1,
		},
		Name: "one", Tool: "github:owner/one", Version: "1.0.0",
	}
	result, err := InstallContextBinaries(t.Context(), t.TempDir(), []pkgplugins.PluginBinarySpec{spec})
	if err != nil {
		t.Fatalf("InstallContextBinaries: %v", err)
	}
	if len(result.Plan.Selections) != 1 || result.Plan.Selections[0].Package.PluginID != "" {
		t.Fatalf("user-only context selection must still publish an empty selection: %+v", result.Plan)
	}
}

func TestInstallContextBinariesDoesNotExposeMiseWhenDisabled(t *testing.T) {
	stellaHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stellaHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mise", ".stella-shell-env"} {
		if err := os.WriteFile(filepath.Join(stellaHome, "bin", name), []byte("internal"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := InstallContextBinaries(t.Context(), stellaHome, nil)
	if err != nil {
		t.Fatalf("InstallContextBinaries: %v", err)
	}
	plan := result.Plan.Selections[0]
	if _, err := os.Stat(filepath.Join(plan.PublicBinDir, "mise")); !os.IsNotExist(err) {
		t.Fatalf("disabled mise selection exposed mise, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.PublicBinDir, ".stella-shell-env")); err != nil {
		t.Fatalf("disabled mise selection lost runner PATH restoration file: %v", err)
	}
	env := OverlayBinaryInstallPlan(map[string]string{"PATH": plan.PublicBinDir}, result.Plan, BinarySystemLayer)
	cmd := exec.Command("/bin/sh", "-c", "command -v mise")
	cmd.Env = append(os.Environ(), "PATH="+env["PATH"])
	if result, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("disabled mise remained discoverable on PATH: %q", result)
	}
}

func TestInstallSandboxBinariesUsesSessionOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	workspace := t.TempDir()
	userTools := filepath.Join(stellaHome, "users", "u", ".mise-tools")
	if err := os.MkdirAll(filepath.Join(stellaHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nset -eu\ncase \"$1\" in trust|reshim) exit 0;; install) mkdir -p \"$MISE_DATA_DIR/installs/user-tool/3.0.0/bin\"; printf '#!/bin/sh\\necho user\\n' > \"$MISE_DATA_DIR/installs/user-tool/3.0.0/bin/user-tool\"; chmod 755 \"$MISE_DATA_DIR/installs/user-tool/3.0.0/bin/user-tool\";; where) printf '%s\\n' \"$MISE_DATA_DIR/installs/user-tool/3.0.0\";; which) printf '%s\\n' \"$MISE_DATA_DIR/installs/user-tool/3.0.0/bin/user-tool\";; *) exit 9;; esac\n"
	if err := os.WriteFile(filepath.Join(stellaHome, "bin", "mise"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userTools, 0o700); err != nil {
		t.Fatal(err)
	}
	factory := nonebackend.NewFactoryWithMountSources(map[string]string{
		pkgsandbox.MountWorkspace:                           workspace,
		pkgsandbox.MountStellaHome + "/bin":                 filepath.Join(stellaHome, "bin"),
		pkgsandbox.MountStellaHome + "/.mise-tools":         filepath.Join(stellaHome, ".mise-tools"),
		pkgsandbox.MountStellaHome + "/users/u/.mise-tools": userTools,
	}, nonebackend.Config{StellaHome: stellaHome})
	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkingDir: pkgsandbox.MountWorkspace,
			Mounts: []pkgsandbox.Mount{
				{SandboxPath: pkgsandbox.MountWorkspace, Access: pkgsandbox.MountReadWrite},
				{SandboxPath: pkgsandbox.MountStellaHome + "/bin", Access: pkgsandbox.MountReadOnly},
				{SandboxPath: pkgsandbox.MountStellaHome + "/.mise-tools", Access: pkgsandbox.MountReadOnly},
				{SandboxPath: pkgsandbox.MountStellaHome + "/users/u/.mise-tools", Access: pkgsandbox.MountReadWrite},
			},
		},
		Env: map[string]string{
			"STELLA_HOME":                 stellaHome,
			"MISE_DATA_DIR":               userTools,
			"MISE_NOT_FOUND_AUTO_INSTALL": "true",
			"PATH":                        filepath.Join(stellaHome, "bin") + string(filepath.ListSeparator) + "/usr/bin",
		},
	}
	session, err := factory.CreateSession(t.Context(), policy)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() { _ = session.Close() }()
	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "user", ConfigID: "cfg-user", Scope: string(plugin.ScopeUser), Revision: 2,
		},
		Name: "user-tool", Tool: "github:owner/user-tool", Version: "3.0.0",
	}
	originalPath := session.Policy().Env["PATH"]
	result, err := InstallSandboxBinaries(t.Context(), session, []pkgplugins.PluginBinarySpec{spec})
	if err != nil {
		t.Fatalf("InstallSandboxBinaries: %v", err)
	}
	if len(result.Plan.Selections) != 1 {
		t.Fatalf("returned plan must expose one exact public selection: %+v", result.Plan)
	}
	plan := result.Plan.Selections[0]
	if _, err := session.Files().Stat(filepath.Join(plan.PublicDir, "user-tool")); err != nil {
		t.Fatalf("sandbox public selection was not materialized: %v", err)
	}
	if got := session.Policy().Env["PATH"]; got != originalPath {
		t.Fatalf("session policy unexpectedly mutated in place: %q", got)
	}
}

func TestInstallContextErrorsHideStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nset -eu\ncase \"$1\" in\ntrust|install) printf 'secret-install-output-%0100d\\n' 0 >&2; printf 'secret-install-stdout-%0100d\\n' 0; exit 0;;\nwhere) printf 'secret-where-output-%0100d\\n' 0 >&2; exit 23;;\n*) exit 9;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "mise"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := pkgplugins.PluginBinarySpec{
		PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
			PluginID: "secret", ConfigID: "cfg-secret", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
		Name: "secret-tool", Tool: "github:owner/secret-tool", Version: "1.0.0",
	}
	result, err := InstallContextBinaries(t.Context(), stellaHome, []pkgplugins.PluginBinarySpec{spec})
	if err != nil || len(result.FailedPackages) != 1 {
		t.Fatalf("InstallContextBinaries result = %+v, error = %v; want one failed package", result, err)
	}
	failure := result.FailedPackages[0].Err
	if strings.Contains(failure.Error(), "secret-install-output") || strings.Contains(failure.Error(), "secret-install-stdout") || strings.Contains(failure.Error(), "secret-where-output") || strings.Contains(failure.Error(), spec.Tool) || len(failure.Error()) > 256 {
		t.Fatalf("InstallContextBinaries failure = %v, want bounded closed error", failure)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
