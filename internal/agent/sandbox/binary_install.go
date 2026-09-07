package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/platform/toolinstall"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// BinaryInstallPlan identifies one complete CLI selection. Its identity is
// derived from every selected binary, including scope and revision, so two
// runners cannot overwrite one another's selection tree.
type BinaryInstallPlan struct {
	Identity     string
	DataDir      string
	PublicDir    string
	PublicBinDir string
}

// BinaryConfigLayer selects which mise precedence layer a plan represents.
type BinaryConfigLayer uint8

const (
	BinarySystemLayer BinaryConfigLayer = iota
	BinaryUserLayer
)

// ContextBinaryInstallPlan returns the host-visible paths for one selected
// binary set. Selection identity is intentionally owned by the sandbox layer,
// where authorization and resource revisions are available.
func ContextBinaryInstallPlan(stellaHome string, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	if stellaHome == "" {
		return BinaryInstallPlan{}, errors.New("sandbox: stella home is required")
	}
	identity, err := binarySelectionIdentity(specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	dataDir := pkgsandbox.MiseToolsDir(stellaHome)
	return BinaryInstallPlan{
		Identity:     identity,
		DataDir:      dataDir,
		PublicDir:    filepath.Join(dataDir, "public", identity),
		PublicBinDir: filepath.Join(dataDir, "public", identity),
	}, nil
}

// InstallContextBinaries installs authorized system selections through the
// generic tool installer. The adapter here is where plugin resource scopes are
// filtered and converted into identity-free Tool values.
func InstallContextBinaries(ctx context.Context, stellaHome string, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	plan, err := ContextBinaryInstallPlan(stellaHome, specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	tools, err := miseToolsFromSpecs(specs, func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.Scope == string(plugin.ScopeSystem) || spec.Scope == string(plugin.ScopeSystemAgent)
	})
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	selection := toolinstall.Selection{
		DataDir: plan.DataDir, PublicDir: plan.PublicDir, PublicBinDir: plan.PublicBinDir,
	}
	if err := toolinstall.InstallSelection(ctx, stellaHome, selection, tools); err != nil {
		return BinaryInstallPlan{}, err
	}
	return plan, nil
}

// InstallSandboxBinaries installs user selections through the already-created
// sandbox session. The host supplies only the validated selection and tools;
// mise, its config, and its cache writes stay inside the session capability.
func InstallSandboxBinaries(ctx context.Context, session pkgsandbox.Session, specs []pkgplugins.PluginBinarySpec) (BinaryInstallPlan, error) {
	if session == nil {
		return BinaryInstallPlan{}, errors.New("sandbox: sandbox session is required")
	}
	identity, err := binarySelectionIdentity(specs)
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	baseEnv := session.Policy().Env
	dataDir := baseEnv["MISE_DATA_DIR"]
	if dataDir == "" || baseEnv["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		return BinaryInstallPlan{}, errors.New("sandbox: user CLI install requires a writable sandbox mise home")
	}
	root := filepath.Join(dataDir, "contexts", identity)
	plan := BinaryInstallPlan{
		Identity:     identity,
		DataDir:      dataDir,
		PublicDir:    filepath.Join(dataDir, "public", identity),
		PublicBinDir: filepath.Join(dataDir, "public", identity),
	}
	tools, err := miseToolsFromSpecs(specs, func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.Scope == string(plugin.ScopeUser) || spec.Scope == string(plugin.ScopeUserAgent)
	})
	if err != nil {
		return BinaryInstallPlan{}, err
	}
	selection := toolinstall.Selection{
		DataDir: plan.DataDir, ConfigPath: filepath.Join(root, "config.toml"), ShimsDir: filepath.Join(root, "shims"),
		PublicDir: plan.PublicDir, PublicBinDir: plan.PublicBinDir,
	}
	if err := toolinstall.InstallSession(ctx, session, selection, tools); err != nil {
		return BinaryInstallPlan{}, err
	}
	return plan, nil
}

// OverlayBinaryInstallPlan applies a completed plan to a runner environment.
func OverlayBinaryInstallPlan(base map[string]string, plan BinaryInstallPlan, layer BinaryConfigLayer) map[string]string {
	env := maps.Clone(base)
	if plan.PublicBinDir != "" {
		if layer == BinarySystemLayer {
			clearNativeMisePaths(env)
		}
		if layer == BinaryUserLayer {
			env[pkgsandbox.EnvUserNativeSelectionDir] = plan.PublicBinDir
		} else {
			env[pkgsandbox.EnvNativeSelectionDir] = plan.PublicBinDir
		}
		env["PATH"] = prependPath(env["PATH"], plan.PublicBinDir)
	}
	// The private preparation session's config and shims never cross into the
	// final runner, even when a caller supplies a stale base environment.
	delete(env, "MISE_SHIMS_DIR")
	env[pkgsandbox.EnvRunnerPath] = env["PATH"]
	return env
}

func clearNativeMisePaths(env map[string]string) {
	for _, key := range []string{"MISE_SHIMS_DIR", "MISE_TRUSTED_CONFIG_PATHS", "MISE_SYSTEM_CONFIG_FILE"} {
		delete(env, key)
	}
	if env["MISE_NOT_FOUND_AUTO_INSTALL"] == "true" {
		return
	}
	for _, key := range []string{"MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_GLOBAL_CONFIG_FILE"} {
		delete(env, key)
	}
}

func prependPath(pathValue, entry string) string {
	if entry == "" {
		return pathValue
	}
	if pathValue == "" {
		return entry
	}
	return entry + string(filepath.ListSeparator) + pathValue
}

func miseToolsFromSpecs(specs []pkgplugins.PluginBinarySpec, keep func(pkgplugins.PluginBinarySpec) bool) ([]toolinstall.Tool, error) {
	tools := make([]toolinstall.Tool, 0, len(specs))
	seen := make(map[string]toolinstall.Tool)
	for _, spec := range specs {
		if !keep(spec) {
			continue
		}
		if spec.Name == "" || spec.Tool == "" {
			return nil, fmt.Errorf("sandbox: binary %q has incomplete identity", spec.Name)
		}
		tool := toolinstall.Tool{Key: spec.Tool, Version: spec.Version, Options: maps.Clone(spec.Options), Lookup: binaryLookupName(spec.Name, spec.Options), PublicName: spec.Name}
		if previous, exists := seen[tool.Key]; exists && !reflect.DeepEqual(previous, tool) {
			return nil, fmt.Errorf("sandbox: selected binaries disagree on mise tool %q", tool.Key)
		}
		if _, exists := seen[tool.Key]; !exists {
			seen[tool.Key] = tool
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func binaryLookupName(name string, options map[string]any) string {
	if value, ok := options["rename_exe"].(string); ok && value != "" {
		return value
	}
	if value, ok := options["bin"].(string); ok && value != "" {
		return value
	}
	return name
}

func binarySelectionIdentity(specs []pkgplugins.PluginBinarySpec) (string, error) {
	canonical := slices.Clone(specs)
	slices.SortFunc(canonical, func(left, right pkgplugins.PluginBinarySpec) int {
		for _, pair := range [][2]string{{left.PluginID, right.PluginID}, {left.Name, right.Name}, {left.Tool, right.Tool}, {left.Version, right.Version}, {left.ConfigID, right.ConfigID}, {left.Scope, right.Scope}} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		return cmpRevision(left.Revision, right.Revision)
	})
	for _, spec := range canonical {
		if spec.PluginID == "" || spec.ConfigID == "" || spec.Scope == "" || spec.Name == "" || spec.Tool == "" {
			return "", fmt.Errorf("sandbox: binary %q is missing resource identity", spec.Name)
		}
		if err := validateBinaryName(spec.Name); err != nil {
			return "", fmt.Errorf("sandbox: binary %q: %w", spec.Name, err)
		}
		switch spec.Scope {
		case string(plugin.ScopeSystem), string(plugin.ScopeSystemAgent), string(plugin.ScopeUser), string(plugin.ScopeUserAgent):
		default:
			return "", fmt.Errorf("sandbox: binary %q has unknown resource scope %q", spec.Name, spec.Scope)
		}
		if spec.Revision <= 0 {
			return "", fmt.Errorf("sandbox: binary %q has non-positive config revision", spec.Name)
		}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("sandbox: encode binary selection identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16]), nil
}

func cmpRevision(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func validateBinaryName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return errors.New("has unsafe path name")
	}
	if strings.ContainsRune(name, 0) {
		return errors.New("contains NUL")
	}
	return nil
}
