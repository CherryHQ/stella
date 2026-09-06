// Package system owns Stella's release-provided runtime commands.
package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
	"github.com/CherryHQ/stella/resources/binaries"
)

// RuntimeResource describes a release-owned command that is available to
// every session. Mise and Xberg are embedded in the release; fd and rg are
// installed into the release-owned mise cache at fixed versions. This catalog
// is independent of plugin state and snapshot identities.
type RuntimeResource struct {
	Name      string
	MiseTool  string
	Version   string
	Options   map[string]any
	Embedded  bool
	SkillRefs []string
}

// RuntimePlan is the immutable system command selection exposed to startup and
// sandbox backends. PublicBinDir is the only directory that needs to be added
// to a runner PATH.
type RuntimePlan struct {
	Identity     string
	PublicDir    string
	PublicBinDir string
	Runtimes     []Runtime
}

// Runtime is one command in a prepared system selection.
type Runtime struct {
	Name      string
	Version   string
	Path      string
	Available bool
}

// RuntimeIdentity returns the content identity of the system declaration and
// embedded release assets for this platform. Embedded digests prevent a
// changed release asset from reusing an older public selection directory.
func RuntimeIdentity() (string, error) { return cachedRuntimeIdentity() }

var embeddedRuntimeAssets = sync.OnceValues(binaries.EmbeddedRuntimeAssets)

var cachedRuntimeIdentity = sync.OnceValues(func() (string, error) {
	assets, err := embeddedRuntimeAssets()
	if err != nil {
		return "", err
	}
	return runtimeIdentity(RuntimeResources(), assets)
})

type runtimeIdentityInput struct {
	OS        string                          `json:"os"`
	Arch      string                          `json:"arch"`
	Resources []RuntimeResource               `json:"resources"`
	Assets    []binaries.EmbeddedRuntimeAsset `json:"assets"`
}

func runtimeIdentity(resources []RuntimeResource, assets []binaries.EmbeddedRuntimeAsset) (string, error) {
	canonicalAssets := slices.Clone(assets)
	slices.SortFunc(canonicalAssets, func(left, right binaries.EmbeddedRuntimeAsset) int {
		return strings.Compare(left.Name, right.Name)
	})
	payload, err := json.Marshal(runtimeIdentityInput{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Resources: resources, Assets: canonicalAssets,
	})
	if err != nil {
		return "", fmt.Errorf("encode system runtime identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "system-" + hex.EncodeToString(digest[:16]), nil
}

// Prepare extracts embedded runtimes and installs the fixed mise-owned system
// tools into one content-addressed public selection. It never creates plugin
// configuration or snapshot identity.
func Prepare(ctx context.Context, stellaHome string) (RuntimePlan, error) {
	if stellaHome == "" {
		return RuntimePlan{}, errors.New("system: stella home is required")
	}
	if err := binaries.EnsureTools(stellaHome); err != nil {
		return RuntimePlan{}, fmt.Errorf("system: ensure embedded runtimes: %w", err)
	}
	// Sandbox mounts resolve STELLA_HOME physically; publish in the same frame.
	resolvedHome, err := filepath.EvalSymlinks(stellaHome)
	if err != nil {
		return RuntimePlan{}, fmt.Errorf("system: resolve stella home: %w", err)
	}
	stellaHome = resolvedHome
	identity, err := RuntimeIdentity()
	if err != nil {
		return RuntimePlan{}, err
	}
	dataDir := filepath.Join(stellaHome, ".mise-tools")
	publicDir := filepath.Join(dataDir, "public", identity)
	tools := make([]manifest.NativeMiseTool, 0, 2)
	embeddedNames := make([]string, 0, 2)
	for _, resource := range RuntimeResources() {
		if resource.Embedded {
			embeddedNames = append(embeddedNames, resource.Name)
		}
		if resource.MiseTool == "" {
			continue
		}
		publicName := resource.Name
		if runtime.GOOS == "windows" {
			publicName += ".exe"
		}
		tools = append(tools, manifest.NativeMiseTool{
			Key: resource.MiseTool, Version: resource.Version, Options: resource.Options,
			Lookup: manifest.BinaryLookupName(manifest.ManifestBinary{Name: resource.Name, Options: resource.Options}), PublicName: publicName,
		})
	}
	if err := manifest.InstallNativeMiseSelection(ctx, stellaHome, manifest.NativeSelectionPlan{
		DataDir: dataDir, PublicDir: publicDir, PublicBinDir: publicDir, EmbeddedNames: embeddedNames,
	}, tools); err != nil {
		return RuntimePlan{}, fmt.Errorf("system: prepare native selection: %w", err)
	}
	plan := runtimePlan(identity, publicDir)
	if err := Verify(plan); err != nil {
		return RuntimePlan{}, err
	}
	return plan, nil
}

// Verify checks that a prepared plan still names the current release assets
// and exposes every system runtime available on this platform.
func Verify(plan RuntimePlan) error {
	if plan.Identity == "" || plan.PublicDir == "" || plan.PublicBinDir == "" {
		return errors.New("system: incomplete runtime plan")
	}
	identity, err := RuntimeIdentity()
	if err != nil {
		return err
	}
	if plan.Identity != identity {
		return fmt.Errorf("system: runtime plan identity %q is stale", plan.Identity)
	}
	assets, err := embeddedRuntimeAssets()
	if err != nil {
		return err
	}
	assetNames := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		assetNames[asset.Name] = struct{}{}
	}
	preparedByName := make(map[string]Runtime, len(plan.Runtimes))
	for _, prepared := range plan.Runtimes {
		if _, exists := preparedByName[prepared.Name]; exists {
			return fmt.Errorf("system: duplicate runtime %q", prepared.Name)
		}
		preparedByName[prepared.Name] = prepared
	}
	if len(preparedByName) != len(RuntimeResources()) {
		return errors.New("system: runtime plan is incomplete")
	}
	for _, resource := range RuntimeResources() {
		prepared, ok := preparedByName[resource.Name]
		if !ok {
			return fmt.Errorf("system: runtime %q is not declared in plan", resource.Name)
		}
		if !prepared.Available && resource.MiseTool == "" {
			assetName := resource.Name
			if resource.Name == "mise" && runtime.GOOS == "windows" {
				assetName = "mise.exe"
			}
			if _, embedded := assetNames[assetName]; !embedded {
				continue
			}
		}
		publicName := prepared.Name
		if runtime.GOOS == "windows" {
			publicName += ".exe"
		}
		if prepared.Path != filepath.Join(plan.PublicBinDir, publicName) {
			return fmt.Errorf("system: runtime %q has invalid path", prepared.Name)
		}
		info, err := os.Stat(prepared.Path)
		if err != nil {
			return fmt.Errorf("system: runtime %q is unavailable: %w", prepared.Name, err)
		}
		if info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
			return fmt.Errorf("system: runtime %q is not executable", prepared.Name)
		}
	}
	return nil
}

// UnavailableSkillRefs returns system skill references whose release runtime is
// absent on this platform. Embedded assets are the only source of truth; a
// missing metadata read fails closed by hiding the affected runtime skill.
func UnavailableSkillRefs() []string {
	assets, err := embeddedRuntimeAssets()
	available := make(map[string]struct{}, len(assets))
	if err == nil {
		for _, asset := range assets {
			available[asset.Name] = struct{}{}
		}
	}
	var unavailable []string
	for _, resource := range RuntimeResources() {
		if !resource.Embedded || len(resource.SkillRefs) == 0 {
			continue
		}
		assetName := resource.Name
		if resource.Name == "mise" && runtime.GOOS == "windows" {
			assetName = "mise.exe"
		}
		if _, ok := available[assetName]; !ok {
			unavailable = append(unavailable, resource.SkillRefs...)
		}
	}
	return unavailable
}

func runtimePlan(identity, publicDir string) RuntimePlan {
	plan := RuntimePlan{
		Identity: identity, PublicDir: publicDir, PublicBinDir: publicDir,
		Runtimes: make([]Runtime, 0, len(RuntimeResources())),
	}
	for _, resource := range RuntimeResources() {
		name := resource.Name
		publicName := name
		if runtime.GOOS == "windows" {
			publicName += ".exe"
		}
		path := filepath.Join(publicDir, publicName)
		_, err := os.Stat(path)
		plan.Runtimes = append(plan.Runtimes, Runtime{
			Name: name, Version: resource.Version, Path: path, Available: err == nil,
		})
	}
	return plan
}
