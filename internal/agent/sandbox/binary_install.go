package sandbox

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"runtime"
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
	Identity   string
	DataDir    string
	Selections []BinarySelectionPlan
}

// BinaryPackage identifies the immutable package resource that owns a binary.
// A package can declare several binaries; it is ready only when all of them
// publish successfully.
type BinaryPackage struct {
	pkgplugins.PluginResourceIdentity
	PackageDigest string
}

// BinarySelectionPlan describes one package-owned immutable selection tree.
type BinarySelectionPlan struct {
	Package      BinaryPackage
	Identity     string
	DataDir      string
	PublicDir    string
	PublicBinDir string
}

// BinaryInstallFailure records a package-scoped preparation error. A failed
// package is omitted from the resulting plan; other packages remain usable.
type BinaryInstallFailure struct {
	Package BinaryPackage
	Err     error
}

// BinaryInstallResult is the package-scoped outcome of CLI preparation.
type BinaryInstallResult struct {
	Plan               BinaryInstallPlan
	SuccessfulPackages []BinaryPackage
	FailedPackages     []BinaryInstallFailure
}

// PreparationResult projects package-scoped CLI outcomes into the shared
// runtime result. It contains no command output or credential material.
func (r BinaryInstallResult) PreparationResult() pkgplugins.PluginPreparationResult {
	result := pkgplugins.PluginPreparationResult{Packages: make([]pkgplugins.PluginPackageStatus, 0, len(r.SuccessfulPackages)+len(r.FailedPackages))}
	for _, pkg := range r.SuccessfulPackages {
		if pkg.PluginID == "" {
			continue
		}
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: pkg.PluginID, Ready: true})
	}
	for _, failure := range r.FailedPackages {
		if failure.Package.PluginID == "" {
			continue
		}
		result.Packages = append(result.Packages, pkgplugins.PluginPackageStatus{PluginID: failure.Package.PluginID, Reason: "CLI preparation failed"})
	}
	return result
}

// BinaryConfigLayer selects which mise precedence layer a plan represents.
type BinaryConfigLayer uint8

const (
	BinarySystemLayer BinaryConfigLayer = iota
	BinaryUserLayer
)

// InstallContextBinaries installs each authorized system package into its own
// immutable public tree. Candidate conflicts are checked before any
// installation so a failed package cannot change the interpretation of the
// remaining packages.
func InstallContextBinaries(ctx context.Context, stellaHome string, specs []pkgplugins.PluginBinarySpec) (BinaryInstallResult, error) {
	if stellaHome == "" {
		return BinaryInstallResult{}, errors.New("sandbox: stella home is required")
	}
	dataDir := pkgsandbox.MiseToolsDir(stellaHome)
	identity, err := binarySelectionIdentity(specs, dataDir)
	if err != nil {
		return BinaryInstallResult{}, err
	}
	if err := validateBinaryCandidates(specs); err != nil {
		return BinaryInstallResult{}, err
	}
	plan := BinaryInstallPlan{Identity: identity, DataDir: dataDir}
	result := BinaryInstallResult{Plan: plan}
	groups := groupedBinarySpecs(specs, isSystemBinary)
	if len(groups) == 0 {
		selection := selectionPlan(identity, BinaryPackage{}, dataDir, filepath.Join(dataDir, "public"))
		if err := toolinstall.InstallSelection(ctx, stellaHome, toolinstall.Selection{
			DataDir: dataDir, PublicDir: selection.PublicDir, PublicBinDir: selection.PublicBinDir,
		}, nil); err != nil {
			return BinaryInstallResult{}, err
		}
		result.Plan.Selections = []BinarySelectionPlan{selection}
		return result, nil
	}
	for _, group := range groups {
		packageIdentity, err := binarySelectionIdentity(group.specs, dataDir)
		if err != nil {
			return BinaryInstallResult{}, err
		}
		selection := selectionPlan(packageIdentity, group.pkg, dataDir, filepath.Join(dataDir, "public"))
		tools, err := miseToolsFromSpecs(group.specs, isSystemBinary)
		if err != nil {
			return BinaryInstallResult{}, err
		}
		if err := toolinstall.InstallSelection(ctx, stellaHome, toolinstall.Selection{
			DataDir: dataDir, PublicDir: selection.PublicDir, PublicBinDir: selection.PublicBinDir,
		}, tools); err != nil {
			result.FailedPackages = append(result.FailedPackages, BinaryInstallFailure{Package: group.pkg, Err: err})
			continue
		}
		result.SuccessfulPackages = append(result.SuccessfulPackages, group.pkg)
		result.Plan.Selections = append(result.Plan.Selections, selection)
	}
	return result, nil
}

// InstallSandboxBinaries installs each user package through the already-created
// sandbox session. Each package receives an isolated config and public tree.
func InstallSandboxBinaries(ctx context.Context, session pkgsandbox.Session, specs []pkgplugins.PluginBinarySpec) (BinaryInstallResult, error) {
	if session == nil {
		return BinaryInstallResult{}, errors.New("sandbox: sandbox session is required")
	}
	baseEnv := session.Policy().Env
	dataDir := baseEnv["MISE_DATA_DIR"]
	if dataDir == "" || baseEnv["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		return BinaryInstallResult{}, errors.New("sandbox: user CLI install requires a writable sandbox mise home")
	}
	identity, err := binarySelectionIdentity(specs, dataDir)
	if err != nil {
		return BinaryInstallResult{}, err
	}
	if err := validateBinaryCandidates(specs); err != nil {
		return BinaryInstallResult{}, err
	}
	plan := BinaryInstallPlan{Identity: identity, DataDir: dataDir}
	result := BinaryInstallResult{Plan: plan}
	for _, group := range groupedBinarySpecs(specs, isUserBinary) {
		packageIdentity, err := binarySelectionIdentity(group.specs, dataDir)
		if err != nil {
			return BinaryInstallResult{}, err
		}
		root := filepath.Join(dataDir, "contexts", packageIdentity)
		selection := selectionPlan(packageIdentity, group.pkg, dataDir, filepath.Join(dataDir, "public"))
		tools, err := miseToolsFromSpecs(group.specs, isUserBinary)
		if err != nil {
			return BinaryInstallResult{}, err
		}
		if err := toolinstall.InstallSession(ctx, session, toolinstall.Selection{
			DataDir: plan.DataDir, ConfigPath: filepath.Join(root, "config.toml"), ShimsDir: filepath.Join(root, "shims"),
			PublicDir: selection.PublicDir, PublicBinDir: selection.PublicBinDir,
		}, tools); err != nil {
			result.FailedPackages = append(result.FailedPackages, BinaryInstallFailure{Package: group.pkg, Err: err})
			continue
		}
		result.SuccessfulPackages = append(result.SuccessfulPackages, group.pkg)
		result.Plan.Selections = append(result.Plan.Selections, selection)
	}
	return result, nil
}

// OverlayBinaryInstallPlan applies a completed plan to a runner environment.
func OverlayBinaryInstallPlan(base map[string]string, plan BinaryInstallPlan, layer BinaryConfigLayer) map[string]string {
	env := maps.Clone(base)
	publicDirs := plan.publicBinDirs()
	if len(publicDirs) > 0 {
		if layer == BinarySystemLayer {
			clearNativeMisePaths(env)
		}
		if layer == BinaryUserLayer {
			env[pkgsandbox.EnvUserNativeSelectionDir] = strings.Join(publicDirs, string(filepath.ListSeparator))
		} else {
			env[pkgsandbox.EnvNativeSelectionDir] = strings.Join(publicDirs, string(filepath.ListSeparator))
		}
		for i := len(publicDirs) - 1; i >= 0; i-- {
			env["PATH"] = prependPath(env["PATH"], publicDirs[i])
		}
	}
	// The private preparation session's config and shims never cross into the
	// final runner, even when a caller supplies a stale base environment.
	delete(env, "MISE_SHIMS_DIR")
	env[pkgsandbox.EnvRunnerPath] = env["PATH"]
	return env
}

func (plan BinaryInstallPlan) publicBinDirs() []string {
	dirs := make([]string, 0, len(plan.Selections))
	for _, selection := range plan.Selections {
		if selection.PublicBinDir != "" {
			dirs = append(dirs, selection.PublicBinDir)
		}
	}
	return dirs
}

func selectionPlan(identity string, pkg BinaryPackage, dataDir, publicRoot string) BinarySelectionPlan {
	publicDir := filepath.Join(publicRoot, identity)
	return BinarySelectionPlan{
		Package:      pkg,
		Identity:     identity,
		DataDir:      dataDir,
		PublicDir:    publicDir,
		PublicBinDir: publicDir,
	}
}

func isSystemBinary(spec pkgplugins.PluginBinarySpec) bool {
	return spec.Scope == string(plugin.ScopeSystem) || spec.Scope == string(plugin.ScopeSystemAgent)
}

func isUserBinary(spec pkgplugins.PluginBinarySpec) bool {
	return spec.Scope == string(plugin.ScopeUser) || spec.Scope == string(plugin.ScopeUserAgent)
}

type binarySpecGroup struct {
	pkg   BinaryPackage
	specs []pkgplugins.PluginBinarySpec
}

func groupedBinarySpecs(specs []pkgplugins.PluginBinarySpec, keep func(pkgplugins.PluginBinarySpec) bool) []binarySpecGroup {
	groups := make(map[BinaryPackage][]pkgplugins.PluginBinarySpec)
	for _, spec := range specs {
		if keep(spec) {
			pkg := BinaryPackage{PluginResourceIdentity: spec.PluginResourceIdentity, PackageDigest: spec.PackageDigest}
			groups[pkg] = append(groups[pkg], spec)
		}
	}
	result := make([]binarySpecGroup, 0, len(groups))
	for pkg, group := range groups {
		result = append(result, binarySpecGroup{pkg: pkg, specs: group})
	}
	slices.SortFunc(result, func(left, right binarySpecGroup) int {
		for _, pair := range [][2]string{
			{left.pkg.PluginID, right.pkg.PluginID},
			{left.pkg.ConfigID, right.pkg.ConfigID},
			{left.pkg.Scope, right.pkg.Scope},
			{left.pkg.PackageDigest, right.pkg.PackageDigest},
		} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		return cmp.Compare(left.pkg.Revision, right.pkg.Revision)
	})
	return result
}

func validateBinaryCandidates(specs []pkgplugins.PluginBinarySpec) error {
	tools, err := miseToolsFromSpecs(specs, func(pkgplugins.PluginBinarySpec) bool { return true })
	if err != nil {
		return err
	}
	if _, err := toolinstall.RenderTOML(tools); err != nil {
		return fmt.Errorf("sandbox: validate binary aliases: %w", err)
	}
	return nil
}

// ValidateSelectedBinarySpecs checks aliases and mise keys across the complete
// selected set before OAuth filtering. A failed package cannot silently allow
// a conflicting ready package to take over the same executable name.
func ValidateSelectedBinarySpecs(specs []pkgplugins.PluginBinarySpec) error {
	return validateBinaryCandidates(specs)
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

// binarySelectionIdentity identifies the complete logical
// selection. The coordinate is the principal-owned install root for user
// selections; including it keeps two users from sharing a logical selection
// even when their config revisions and pins happen to match.
func binarySelectionIdentity(specs []pkgplugins.PluginBinarySpec, coordinate string) (string, error) {
	canonical := slices.Clone(specs)
	slices.SortFunc(canonical, func(left, right pkgplugins.PluginBinarySpec) int {
		for _, pair := range [][2]string{{left.PluginID, right.PluginID}, {left.ConfigID, right.ConfigID}, {left.Scope, right.Scope}, {left.PackageDigest, right.PackageDigest}, {left.Name, right.Name}, {left.Tool, right.Tool}, {left.Version, right.Version}} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		return cmp.Compare(left.Revision, right.Revision)
	})
	for _, spec := range canonical {
		if spec.PluginID == "" || spec.ConfigID == "" || spec.Scope == "" || spec.Name == "" || spec.Tool == "" {
			return "", fmt.Errorf("sandbox: binary %q is missing resource identity", spec.Name)
		}
		if err := validateBinaryName(spec.Name); err != nil {
			return "", fmt.Errorf("sandbox: binary %q: %w", spec.Name, err)
		}
		if err := plugin.ValidateBinaryVersion(spec.Version); err != nil {
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
	type selectionBinary struct {
		PluginID      string `json:"plugin_id"`
		ConfigID      string `json:"config_id"`
		Scope         string `json:"scope"`
		Revision      int64  `json:"revision"`
		PackageDigest string `json:"package_digest"`
		Artifact      string `json:"artifact"`
	}
	entries := make([]selectionBinary, 0, len(canonical))
	for _, spec := range canonical {
		artifact, err := pkgplugins.BinaryArtifactIdentity(spec)
		if err != nil {
			return "", fmt.Errorf("sandbox: binary %q artifact identity: %w", spec.Name, err)
		}
		entries = append(entries, selectionBinary{
			PluginID: spec.PluginID, ConfigID: spec.ConfigID, Scope: spec.Scope,
			Revision: spec.Revision, PackageDigest: spec.PackageDigest, Artifact: artifact,
		})
	}
	payload, err := json.Marshal(struct {
		GOOS       string            `json:"goos"`
		GOARCH     string            `json:"goarch"`
		Coordinate string            `json:"coordinate,omitempty"`
		Binaries   []selectionBinary `json:"binaries"`
	}{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Coordinate: coordinate, Binaries: entries,
	})
	if err != nil {
		return "", fmt.Errorf("sandbox: encode binary selection identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16]), nil
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
