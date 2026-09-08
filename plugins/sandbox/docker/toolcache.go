package docker

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sync/singleflight"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

const (
	toolCacheHelperWaitTimeout  = 5 * time.Minute
	toolCacheHelperPollInterval = 2 * time.Second
	toolCacheGCAgeThreshold     = 7 * 24 * time.Hour
	// Keep Docker helper pressure bounded; raise only after measuring daemon load.
	maxConcurrentPackageCaches = 4
)

const (
	// /opt/stella is world-traversable. /home/stella is 0700, so rootless
	// container UID 0 with all capabilities dropped cannot reach mounts there.
	// Selection artifacts are mounted over the image's /opt/stella/bin. Keeping
	// the helper's mount point separate lets it read the image binaries while
	// constructing a volume that contains only this snapshot's selection.
	containerSelectionRoot        = "/opt/stella/selection-tools"
	containerSelectionBin         = "/opt/stella/bin"
	containerPackageSelectionRoot = "/opt/stella/package-tools"
	containerCoreRuntimeRoot      = "/opt/stella/core-runtime"
	containerBuiltinArtifactRoot  = "/opt/stella/.mise-tools/builtin-artifacts"
	containerSelectionReadyMarker = containerSelectionRoot + "/.stella-selection-ready"
)

const (
	toolCacheLabel          = "stella.tool_cache"
	toolCacheImageLabel     = "stella.image"
	toolCacheHashLabel      = "stella.hash"
	toolCacheCreatedAtLabel = "stella.tool_cache.created_at"
	toolCacheKindLabel      = "stella.tool_cache.kind"
)

// ToolBinary describes a user-configured CLI that must be installed in a Linux
// container context before docker sandbox sessions can execute it.
// Selection identity belongs to the runner; tool options are passed to mise.
type ToolBinary struct {
	PluginID string
	ConfigID string
	Scope    string
	Revision int64
	// PackageDigest identifies the immutable published package that declared
	// this selection. It belongs in the selection cache key, while artifact
	// reuse remains based on effective install inputs.
	PackageDigest string
	Name          string
	Tool          string // mise tool key: uv, bun, github:owner/repo, pipx:pkg, npm:pkg, http:name
	Version       string
	Options       map[string]any // mise tool options, using the same names as mise.toml
}

// ToolPackage identifies the package that owns one or more selected tools.
// Keep this type free of internal imports: Docker is a replaceable plugin and
// reports readiness through this small value object to the runtime layer.
type ToolPackage struct {
	PluginID      string
	ConfigID      string
	Scope         string
	Revision      int64
	PackageDigest string
}

// ToolPackageFailure records a package-local preparation error. A failed
// optional package does not poison the other package selections.
type ToolPackageFailure struct {
	Package ToolPackage
	Err     error
}

// ToolPreparationResult describes what the Docker helper made available to a
// session. Core runtime preparation is returned as an error because it is
// required by every verified image; optional package failures are retained.
type ToolPreparationResult struct {
	SuccessfulPackages []ToolPackage
	FailedPackages     []ToolPackageFailure
}

func (b ToolBinary) miseToolKey() string {
	return b.Tool
}

func binaryArtifactIdentity(binary ToolBinary) (string, error) {
	return pkgplugins.BinaryArtifactIdentity(pkgplugins.PluginBinarySpec{
		Name: binary.Name, Tool: binary.Tool, Version: binary.Version, Options: binary.Options,
	})
}

type selectionToolCache struct {
	VolumeName     string
	BinPath        string
	MaskVolumeName string
	RootPath       string
}

type selectionToolCacheSet struct {
	Core        *selectionToolCache
	Packages    []selectionToolCache
	Preparation ToolPreparationResult
}

var (
	toolCacheGroup              singleflight.Group
	installSelectionToolCacheFn = installSelectionToolCache
)

// ensureSelectionToolCache prepares Linux-native artifacts for the immutable
// runner snapshot. The cache key must use the resolved image ID because a tag
// can move while a long-running stellad process is alive.
func ensureSelectionToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, imageID string) (*selectionToolCache, error) {
	return ensureSelectionToolCacheAt(ctx, client, cfg, imageID, containerSelectionRoot, containerSelectionBin)
}

func ensureSelectionToolCacheAt(ctx context.Context, client *dockerclient.Client, cfg Config, imageID, rootPath, binPath string) (*selectionToolCache, error) {
	if imageID == "" {
		return nil, fmt.Errorf("docker selection tool cache: resolved image ID is required")
	}
	if rootPath == "" || binPath == "" {
		return nil, errors.New("docker selection tool cache: selection paths are required")
	}
	core := systemplugins.EmbeddedRuntimeResources()
	if rootPath != containerSelectionRoot {
		core = nil
	}
	hash := selectionToolCacheHash(imageID, cfg.SelectionToolBinaries, core)
	cacheID := toolCachePersistentID(hash)
	volumeName := "stella-selection-" + cacheID
	installerName := "stella-selection-cache-" + cacheID
	cache := &selectionToolCache{VolumeName: volumeName, BinPath: binPath, MaskVolumeName: "stella-selection-mask-" + cacheID, RootPath: rootPath}
	value, err, _ := toolCacheGroup.Do("selection:"+hash, func() (any, error) {
		ready, err := installSelectionToolCacheFn(ctx, client, cfg, imageID, hash, installerName, cache)
		if err != nil {
			return nil, err
		}
		return ready, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*selectionToolCache), nil
}

// ensureSelectionToolCacheSet prepares core runtimes and each package in an
// independent cache. Candidate validation happens before any helper starts so
// a malformed alias cannot make the remaining packages appear successful.
func ensureSelectionToolCacheSet(ctx context.Context, client *dockerclient.Client, cfg Config, imageID string) (*selectionToolCacheSet, error) {
	if err := validateSelectionCandidates(cfg.SelectionToolBinaries); err != nil {
		return nil, err
	}
	coreCfg := cfg
	coreCfg.SelectionToolBinaries = nil
	core, err := ensureSelectionToolCacheAt(ctx, client, coreCfg, imageID, containerSelectionRoot, containerSelectionBin)
	if err != nil {
		return nil, fmt.Errorf("docker core runtime cache: %w", err)
	}
	set := &selectionToolCacheSet{Core: core}
	groups := groupedToolBinaries(cfg.SelectionToolBinaries)
	type packageCacheResult struct {
		cache *selectionToolCache
		err   error
	}
	results := make([]packageCacheResult, len(groups))
	limit := min(maxConcurrentPackageCaches, len(groups))
	if limit == 0 {
		return set, nil
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, group := range groups {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			defer func() { <-sem }()

			packageHash := selectionToolCacheHash(imageID, group.binaries, nil)
			root := containerPackageSelectionRoot + "/" + toolCachePersistentID(packageHash)
			packageCfg := cfg
			packageCfg.SelectionToolBinaries = group.binaries
			cache, installErr := ensureSelectionToolCacheAt(ctx, client, packageCfg, imageID, root, root+"/bin")
			results[i] = packageCacheResult{cache: cache, err: installErr}
		})
	}
	wg.Wait()
	for i, group := range groups {
		result := results[i]
		if result.err != nil {
			set.Preparation.FailedPackages = append(set.Preparation.FailedPackages, ToolPackageFailure{Package: group.packageID, Err: result.err})
			continue
		}
		set.Packages = append(set.Packages, *result.cache)
		set.Preparation.SuccessfulPackages = append(set.Preparation.SuccessfulPackages, group.packageID)
	}
	return set, nil
}

// toolCachePersistentID removes the human-readable prefix while retaining the
// complete digest returned by toolCacheHash for persistent Docker names.
func toolCachePersistentID(hash string) string {
	return strings.TrimPrefix(hash, "selection-")
}

type toolBinaryGroup struct {
	packageID ToolPackage
	binaries  []ToolBinary
}

func groupedToolBinaries(binaries []ToolBinary) []toolBinaryGroup {
	groups := make(map[ToolPackage][]ToolBinary)
	for _, binary := range binaries {
		packageID := ToolPackage{PluginID: binary.PluginID, ConfigID: binary.ConfigID, Scope: binary.Scope, Revision: binary.Revision, PackageDigest: binary.PackageDigest}
		groups[packageID] = append(groups[packageID], binary)
	}
	groupsList := make([]toolBinaryGroup, 0, len(groups))
	for packageID, packageBinaries := range groups {
		groupsList = append(groupsList, toolBinaryGroup{packageID: packageID, binaries: packageBinaries})
	}
	slices.SortFunc(groupsList, func(left, right toolBinaryGroup) int {
		for _, pair := range [][2]string{
			{left.packageID.PluginID, right.packageID.PluginID},
			{left.packageID.ConfigID, right.packageID.ConfigID},
			{left.packageID.Scope, right.packageID.Scope},
			{left.packageID.PackageDigest, right.packageID.PackageDigest},
		} {
			if pair[0] != pair[1] {
				return strings.Compare(pair[0], pair[1])
			}
		}
		return cmp.Compare(left.packageID.Revision, right.packageID.Revision)
	})
	return groupsList
}

func installSelectionToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, imageID, hash, installerName string, cache *selectionToolCache) (*selectionToolCache, error) {
	core := cacheCoreRuntimeResources(cache)
	rootPath := cacheRootPath(cache)
	if _, err := client.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Name: cache.VolumeName,
		Labels: map[string]string{
			toolCacheLabel:          "true",
			toolCacheKindLabel:      "selection",
			toolCacheImageLabel:     imageID,
			toolCacheHashLabel:      hash,
			toolCacheCreatedAtLabel: time.Now().UTC().Format(time.RFC3339),
		},
	}); err != nil {
		return nil, fmt.Errorf("docker selection tool cache: create volume %s: %w", cache.VolumeName, err)
	}
	if rootPath == containerSelectionRoot {
		if _, err := client.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
			Name: cache.MaskVolumeName,
			Labels: map[string]string{
				toolCacheLabel:          "true",
				toolCacheKindLabel:      "selection-mask",
				toolCacheImageLabel:     imageID,
				toolCacheHashLabel:      hash,
				toolCacheCreatedAtLabel: time.Now().UTC().Format(time.RFC3339),
			},
		}); err != nil {
			return nil, fmt.Errorf("docker selection tool cache: create mask volume %s: %w", cache.MaskVolumeName, err)
		}
	} else {
		cache.MaskVolumeName = ""
	}
	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image:       imageID,
		Runtime:     cfg.Runtime,
		NetworkMode: dockerclient.NetworkAllowAll,
		User:        "root",
		Env:         map[string]string{"HOME": "/root"},
		ExtraMounts: []dockerclient.Mount{{
			HostPath: cache.VolumeName, ContainerPath: rootPath,
			ReadOnly: false, Type: dockerclient.MountTypeVolume,
		}, {
			// CreateOptions enforces ReadonlyRootfs. Keep installer config and
			// mise data in a throw-away tmpfs instead of persisting private state.
			ContainerPath: "/tmp", ReadOnly: false, Type: dockerclient.MountTypeTmpfs, TmpfsExec: true,
		}},
		Labels: map[string]string{
			"stella.tool_cache_helper": "true",
			toolCacheLabel:             cache.VolumeName,
			toolCacheKindLabel:         "selection",
		},
		Name: installerName,
	})
	if err != nil {
		if errdefs.IsConflict(err) {
			return waitForToolCache(ctx, client, installerName, cache, func(ctx context.Context) error {
				return verifySelectionToolCache(ctx, client, cfg, imageID, hash, cache)
			})
		}
		return nil, fmt.Errorf("docker selection tool cache: start helper: %w", err)
	}
	defer func() {
		if stopErr := client.Stop(context.Background(), containerID); stopErr != nil {
			slog.Warn("docker selection tool cache helper cleanup failed", "container_id", containerID, "error", stopErr)
		}
	}()

	result, err := client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: containerID,
		Command:     []string{"/bin/sh", "-s"},
		Cwd:         rootPath,
		Stdin:       strings.NewReader(selectionToolInstallScriptAt(rootPath, hash, cfg.SelectionToolBinaries, core)),
	})
	if err != nil {
		return nil, fmt.Errorf("docker selection tool cache: run installer: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("docker selection tool cache: installer failed with exit %d", result.ExitCode)
	}
	return cache, nil
}

func verifySelectionToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, imageID, hash string, cache *selectionToolCache) error {
	core := cacheCoreRuntimeResources(cache)
	rootPath := cacheRootPath(cache)
	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image: imageID, Runtime: cfg.Runtime, NetworkMode: dockerclient.NetworkDisabled, User: "root",
		ExtraMounts: []dockerclient.Mount{{
			HostPath: cache.VolumeName, ContainerPath: rootPath,
			ReadOnly: true, Type: dockerclient.MountTypeVolume,
		}},
		Labels: map[string]string{
			"stella.tool_cache_verifier": "true",
			toolCacheLabel:               cache.VolumeName,
			toolCacheKindLabel:           "selection",
		},
	})
	if err != nil {
		return fmt.Errorf("start selection verifier: %w", err)
	}
	defer func() {
		if stopErr := client.Stop(context.Background(), containerID); stopErr != nil {
			slog.Warn("docker selection tool cache verifier cleanup failed", "container_id", containerID, "error", stopErr)
		}
	}()
	result, err := client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: containerID, Command: []string{"/bin/sh", "-s"}, Cwd: rootPath,
		Stdin: strings.NewReader(selectionToolVerifyScriptAt(rootPath, hash, cfg.SelectionToolBinaries, core)),
	})
	if err != nil {
		return fmt.Errorf("run selection verifier: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("selection verifier failed with exit %d", result.ExitCode)
	}
	return nil
}

func cacheCoreRuntimeResources(cache *selectionToolCache) []systemplugins.RuntimeResource {
	if cache != nil && cache.RootPath == containerSelectionRoot {
		return systemplugins.EmbeddedRuntimeResources()
	}
	return nil
}

func cacheRootPath(cache *selectionToolCache) string {
	if cache == nil {
		return ""
	}
	return cache.RootPath
}

// waitForToolCache waits for a concurrently running installer container to
// finish and returns the cache if it succeeded. Used when another app instance
// already holds the installer container name (the distributed mutex).
func waitForToolCache(ctx context.Context, client *dockerclient.Client, installerName string, cache *selectionToolCache, verify func(context.Context) error) (*selectionToolCache, error) {
	deadline := time.Now().Add(toolCacheHelperWaitTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("docker selection tool cache: timed out waiting for installer %s", installerName)
		}

		state, err := client.InspectContainerState(ctx, installerName)
		if err != nil {
			return nil, fmt.Errorf("docker selection tool cache: inspect installer: %w", err)
		}
		if state == nil {
			// The helper disappearing is ambiguous: Docker removes it after both
			// success and failure, so fail closed unless the volume proves ready.
			if err := verify(ctx); err != nil {
				return nil, fmt.Errorf("docker selection tool cache: installer %s finished but cache is not ready: %w", installerName, err)
			}
			return cache, nil
		}
		if !state.Running {
			if stopErr := client.Stop(context.Background(), installerName); stopErr != nil {
				slog.Warn("docker selection tool cache: cleanup stopped installer", "name", installerName, "error", stopErr)
			}
			if state.ExitCode != 0 {
				return nil, fmt.Errorf("docker selection tool cache: installer %s exited with %d", installerName, state.ExitCode)
			}
			if err := verify(ctx); err != nil {
				return nil, fmt.Errorf("docker selection tool cache: installer %s exited successfully but cache is not ready: %w", installerName, err)
			}
			return cache, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(toolCacheHelperPollInterval):
		}
	}
}

func resetToolCacheStateForTest() {
	toolCacheGroup = singleflight.Group{}
	installSelectionToolCacheFn = installSelectionToolCache
}

func selectionToolCacheHash(imageID string, binaries []ToolBinary, core []systemplugins.RuntimeResource) string {
	return "selection-" + toolCacheHash(imageID, binaries, core)
}

func toolCacheHash(image string, binaries []ToolBinary, core []systemplugins.RuntimeResource) string {
	var buf bytes.Buffer
	buf.WriteString("image=")
	buf.WriteString(image)
	buf.WriteByte('\n')
	for _, b := range canonicalToolBinaries(binaries) {
		fmt.Fprintf(&buf, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t", b.PluginID, b.ConfigID, b.Scope, b.Revision, b.PackageDigest, b.Name, b.Tool)
		buf.WriteString(b.Version)
		buf.WriteByte('\t')
		options, _ := toml.Marshal(b.Options)
		buf.Write(options)
		buf.WriteByte('\n')
	}
	for _, b := range canonicalCoreRuntimeBinaries(core) {
		fmt.Fprintf(&buf, "system\t%v\t%v\t%v\t%t\t%v\t%v\n", b.Name, b.MiseTool, b.Version, b.Embedded, b.SkillRefs, b.Options)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func canonicalToolBinaries(binaries []ToolBinary) []ToolBinary {
	canonical := slices.Clone(binaries)
	slices.SortFunc(canonical, func(left, right ToolBinary) int {
		for _, pair := range [][2]string{
			{left.PluginID, right.PluginID},
			{left.ConfigID, right.ConfigID},
			{left.Scope, right.Scope},
			{fmt.Sprint(left.Revision), fmt.Sprint(right.Revision)},
			{left.PackageDigest, right.PackageDigest},
			{left.Name, right.Name},
			{left.Tool, right.Tool},
			{left.Version, right.Version},
		} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	return canonical
}

func canonicalCoreRuntimeBinaries(binaries []systemplugins.RuntimeResource) []systemplugins.RuntimeResource {
	canonical := slices.Clone(binaries)
	slices.SortFunc(canonical, func(left, right systemplugins.RuntimeResource) int {
		if left.Name < right.Name {
			return -1
		}
		if left.Name > right.Name {
			return 1
		}
		return strings.Compare(left.Version, right.Version)
	})
	return canonical
}

func selectionMiseTOML(binaries []ToolBinary) (string, error) {
	tools := make(map[string]any, len(binaries))
	seen := make(map[string]struct {
		version string
		options map[string]any
	}, len(binaries))
	for _, b := range binaries {
		key := b.miseToolKey()
		if key == "" {
			return "", fmt.Errorf("binary %q: cannot determine mise tool key", b.Name)
		}
		ver := b.Version
		if ver == "" {
			ver = "latest"
		}

		options := maps.Clone(b.Options)
		if options == nil {
			options = make(map[string]any)
		}
		if previous, ok := seen[key]; ok && (previous.version != ver || !reflect.DeepEqual(previous.options, options)) {
			return "", fmt.Errorf("selected binaries disagree on mise tool %q", key)
		}
		seen[key] = struct {
			version string
			options map[string]any
		}{version: ver, options: options}

		var toolValue any = ver
		if len(options) > 0 {
			if _, ok := options["version"]; !ok {
				options["version"] = ver
			}
			toolValue = options
		}
		tools[key] = toolValue
	}
	data, err := toml.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return "", fmt.Errorf("marshal user tools mise.toml: %w", err)
	}
	return string(data), nil
}

func validateSelectionCandidates(binaries []ToolBinary) error {
	if _, err := selectionMiseTOML(binaries); err != nil {
		return fmt.Errorf("docker selection: validate candidates: %w", err)
	}
	byName := make(map[string]string, len(binaries))
	byLookup := make(map[string]string, len(binaries))
	for _, binary := range binaries {
		if !safeSelectionName(binary.Name) {
			return fmt.Errorf("docker selection: invalid binary alias %q", binary.Name)
		}
		if binary.Tool == "" {
			return fmt.Errorf("docker selection: binary %q has empty mise tool key", binary.Name)
		}
		lookup := binary.Name
		if renameExe, ok := stringOption(binary.Options, "rename_exe"); ok {
			lookup = renameExe
		} else if bin, ok := stringOption(binary.Options, "bin"); ok {
			lookup = bin
		}
		if !safeSelectionName(lookup) {
			return fmt.Errorf("docker selection: invalid executable alias %q", lookup)
		}
		if previous, ok := byLookup[lookup]; ok && previous != binary.Name {
			return fmt.Errorf("docker selection: executable alias %q is claimed by %q and %q", lookup, previous, binary.Name)
		}
		byLookup[lookup] = binary.Name
		artifact, err := binaryArtifactIdentity(binary)
		if err != nil {
			return fmt.Errorf("docker selection: binary %q: %w", binary.Name, err)
		}
		if previous, ok := byName[binary.Name]; ok && previous != artifact {
			return fmt.Errorf("docker selection: aliases %q resolve to different artifacts", binary.Name)
		}
		byName[binary.Name] = artifact
	}
	for _, core := range systemplugins.EmbeddedRuntimeResources() {
		if _, ok := byName[core.Name]; ok {
			return fmt.Errorf("docker selection: binary %q conflicts with mandatory core runtime", core.Name)
		}
	}
	return nil
}

// selectionToolInstallScript runs only in the Linux helper container. It uses
// a temporary mise config and data directory, then removes both before marking
// the public volume ready. Published tools are copied as complete install
// directories so launchers can resolve adjacent libraries and other sidecars
// without consulting mise at runner time.
func selectionToolInstallScript(hash string, binaries []ToolBinary, core []systemplugins.RuntimeResource) string {
	return selectionToolInstallScriptAt(containerSelectionRoot, hash, binaries, core)
}

func selectionToolInstallScriptAt(rootPath, hash string, binaries []ToolBinary, core []systemplugins.RuntimeResource) string {
	coreNames := make(map[string]struct{}, len(core))
	for _, binary := range core {
		coreNames[binary.Name] = struct{}{}
	}
	// Several packages may select one identical CLI. Publish it once while
	// retaining every package/config identity in the selection cache key.
	unique := make([]ToolBinary, 0, len(binaries))
	artifactIdentities := make([]string, 0, len(binaries))
	seen := make(map[string]string, len(binaries))
	for _, binary := range binaries {
		identity, err := binaryArtifactIdentity(binary)
		if err != nil {
			return "echo " + shellQuote("binary artifact identity: "+err.Error()) + " >&2\nexit 1\n"
		}
		if previous, exists := seen[binary.Name]; exists {
			if previous != identity {
				return "echo " + shellQuote("selected binaries disagree on command "+binary.Name) + " >&2\nexit 1\n"
			}
			continue
		}
		seen[binary.Name] = identity
		unique = append(unique, binary)
		artifactIdentities = append(artifactIdentities, identity)
	}
	binaries = unique
	miseTOMLs := make([]string, len(binaries))
	if len(binaries) > 0 {
		if _, err := selectionMiseTOML(binaries); err != nil {
			return "echo " + shellQuote(err.Error()) + " >&2\nexit 1\n"
		}
		var err error
		for i, binary := range binaries {
			miseTOMLs[i], err = selectionMiseTOML([]ToolBinary{binary})
			if err != nil {
				return "echo " + shellQuote(err.Error()) + " >&2\nexit 1\n"
			}
		}
	}
	var script strings.Builder
	script.WriteString("set -eu\n")
	script.WriteString("ROOT=" + shellQuote(rootPath) + "\n")
	script.WriteString("PRIVATE=/tmp/stella-selection-private\n")
	script.WriteString("HASH=" + shellQuote(hash) + "\n")
	script.WriteString("STAGING_ROOT=\ntrap 'rm -rf \"$PRIVATE\" \"${STAGING_ROOT:-}\"' EXIT HUP INT TERM\n")
	script.WriteString("# trap 'rm -rf \"$PRIVATE\"' is retained as the private-state cleanup contract.\n")
	script.WriteString("if [ -f \"$ROOT/.stella-selection-ready\" ] && [ \"$(cat \"$ROOT/.stella-selection-ready\")\" = \"$HASH\" ]; then exit 0; fi\n")
	script.WriteString("FINAL_ROOT=\"$ROOT\"\nSTAGING_ROOT=\"$FINAL_ROOT/.stella-selection-staging-$$\"\nROOT=\"$STAGING_ROOT\"\n")
	script.WriteString("rm -rf \"$ROOT\" \"$PRIVATE\"\n")
	script.WriteString("mkdir -p \"$ROOT/bin\" \"$ROOT/core\" \"$ROOT/artifacts\"\n")
	if len(core) > 0 {
		// Copy the complete core plan once. Keeping its relative symlinks and
		// install sidecars intact avoids copying the same mise tree once per alias.
		script.WriteString("cp -R " + containerCoreRuntimeRoot + "/. \"$ROOT/core/\"\n")
		script.WriteString("if [ -f \"$ROOT/core/.stella-shell-env\" ]; then cp \"$ROOT/core/.stella-shell-env\" \"$ROOT/bin/.stella-shell-env\"; fi\n")
	}
	if len(binaries) > 0 {
		// Image-built artifacts are immutable release output. Probe each exact
		// content identity before invoking mise so an offline image hit never
		// falls back to a network install.
		for i, b := range binaries {
			if !safeSelectionName(b.Name) {
				continue
			}
			name := shellQuoteForDoubleQuotedPath(b.Name)
			identity := shellQuoteForDoubleQuotedPath(artifactIdentities[i])
			script.WriteString("if [ -x \"" + containerBuiltinArtifactRoot + "/" + identity + "/" + name + "\" ]; then\n")
			script.WriteString("  mkdir -p \"$ROOT/artifacts/" + identity + "\"\n")
			script.WriteString("  cp -R \"" + containerBuiltinArtifactRoot + "/" + identity + "/.\" \"$ROOT/artifacts/" + identity + "/\"\n")
			script.WriteString("  test -x \"$ROOT/artifacts/" + identity + "/" + name + "\"\n")
			script.WriteString("  ln -s \"$FINAL_ROOT/artifacts/" + identity + "/" + name + "\" \"$ROOT/bin/" + name + "\"\n")
			script.WriteString("fi\n")
		}
	}
	for i, b := range binaries {
		if _, ok := coreNames[b.Name]; ok {
			script.WriteString("echo " + shellQuote("selection binary conflicts with mandatory core runtime "+b.Name) + " >&2\nexit 1\n")
			continue
		}
		if !safeSelectionName(b.Name) {
			script.WriteString("echo " + shellQuote("invalid selection binary name "+b.Name) + " >&2\nexit 1\n")
			continue
		}
		lookup := b.Name
		if renameExe, ok := stringOption(b.Options, "rename_exe"); ok {
			lookup = renameExe
		} else if bin, ok := stringOption(b.Options, "bin"); ok {
			lookup = bin
		}
		if !safeSelectionName(lookup) {
			script.WriteString("echo " + shellQuote("invalid selection executable name "+lookup) + " >&2\nexit 1\n")
			continue
		}
		name := shellQuoteForDoubleQuotedPath(b.Name)
		lookupPath := shellQuoteForDoubleQuotedPath(lookup)
		artifact := shellQuoteForDoubleQuotedPath(artifactIdentities[i])
		miseEnv := "MISE_DATA_DIR=\"$PRIVATE/mise-data\" MISE_CACHE_DIR=\"$PRIVATE/mise-cache\" MISE_STATE_DIR=\"$PRIVATE/mise-state\" MISE_CONFIG_DIR=\"$PRIVATE/mise-config\" MISE_SYSTEM_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_GLOBAL_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_TRUSTED_CONFIG_PATHS=\"$PRIVATE\" XDG_CACHE_HOME=\"$PRIVATE/xdg-cache\" XDG_CONFIG_HOME=\"$PRIVATE/xdg-config\" XDG_DATA_HOME=\"$PRIVATE/xdg-data\" XDG_STATE_HOME=\"$PRIVATE/xdg-state\" "
		script.WriteString("if [ ! -L \"$ROOT/bin/" + name + "\" ]; then\n")
		script.WriteString("  mkdir -p \"$PRIVATE/mise-data\" \"$PRIVATE/mise-cache\" \"$PRIVATE/mise-state\" \"$PRIVATE/mise-config\" \"$PRIVATE/xdg-cache\" \"$PRIVATE/xdg-config\" \"$PRIVATE/xdg-data\" \"$PRIVATE/xdg-state\"\n")
		script.WriteString("  cat > \"$PRIVATE/mise.toml\" <<'STELLA_SELECTION_MISE_TOML_" + fmt.Sprint(i) + "'\n")
		script.WriteString(miseTOMLs[i])
		if !strings.HasSuffix(miseTOMLs[i], "\n") {
			script.WriteByte('\n')
		}
		script.WriteString("STELLA_SELECTION_MISE_TOML_" + fmt.Sprint(i) + "\n")
		script.WriteString("  cd \"$PRIVATE\"\n")
		script.WriteString("  " + miseEnv + containerCoreRuntimeRoot + "/mise trust -y \"$PRIVATE/mise.toml\" >/dev/null 2>&1 || true\n")
		script.WriteString("  " + miseEnv + containerCoreRuntimeRoot + "/mise install\n")
		script.WriteString("  install_dir=$(" + miseEnv + containerCoreRuntimeRoot + "/mise where " + shellQuote(b.miseToolKey()) + ")\n")
		script.WriteString("  test -d \"$install_dir\"\n")
		script.WriteString("  rm -rf \"$ROOT/artifacts/" + artifact + "\"\n  mkdir -p \"$ROOT/artifacts/" + artifact + "\"\n  cp -R \"$install_dir/.\" \"$ROOT/artifacts/" + artifact + "/\"\n")
		script.WriteString("  src=\"\"\nsrc_rel=\"\"\nif [ -f \"$ROOT/artifacts/" + artifact + "/bin/" + lookupPath + "\" ]; then src=\"$ROOT/artifacts/" + artifact + "/bin/" + lookupPath + "\"; src_rel=\"$FINAL_ROOT/artifacts/" + artifact + "/bin/" + lookupPath + "\"; fi\n")
		script.WriteString("  if [ -z \"$src\" ] && [ -f \"$ROOT/artifacts/" + artifact + "/" + lookupPath + "\" ]; then src=\"$ROOT/artifacts/" + artifact + "/" + lookupPath + "\"; src_rel=\"$FINAL_ROOT/artifacts/" + artifact + "/" + lookupPath + "\"; fi\n")
		script.WriteString("  test -n \"$src\" && test -x \"$src\"\n  ln -s \"$src_rel\" \"$ROOT/bin/" + name + "\"\nfi\n")
	}
	for _, coreBinary := range canonicalCoreRuntimeBinaries(core) {
		name := coreBinary.Name
		if !safeSelectionName(name) {
			script.WriteString("echo " + shellQuote("invalid core runtime binary name "+name) + " >&2\nexit 1\n")
			continue
		}
		quoted := shellQuoteForDoubleQuotedPath(name)
		script.WriteString("test -x \"$ROOT/core/" + quoted + "\"\n")
		script.WriteString("ln -s \"$FINAL_ROOT/core/" + quoted + "\" \"$ROOT/bin/" + quoted + "\"\n")
	}
	// Publish only after every selected alias and sidecar passed verification.
	// The final marker is the cache's commit record; a failed staging run leaves
	// the old published tree untouched and never becomes mountable.
	script.WriteString("rm -rf \"$FINAL_ROOT/bin\" \"$FINAL_ROOT/core\" \"$FINAL_ROOT/artifacts\" \"$FINAL_ROOT/.stella-selection-ready\"\n")
	script.WriteString("mv \"$ROOT/bin\" \"$FINAL_ROOT/bin\"\n")
	script.WriteString("mv \"$ROOT/core\" \"$FINAL_ROOT/core\"\n")
	script.WriteString("mv \"$ROOT/artifacts\" \"$FINAL_ROOT/artifacts\"\n")
	script.WriteString("printf '%s' \"$HASH\" > \"$FINAL_ROOT/.stella-selection-ready\"\nchmod 0444 \"$FINAL_ROOT/.stella-selection-ready\"\n")
	return script.String()
}

func selectionToolVerifyScriptAt(rootPath, hash string, binaries []ToolBinary, core []systemplugins.RuntimeResource) string {
	var script strings.Builder
	script.WriteString("set -eu\nROOT=" + shellQuote(rootPath) + "\nHASH=" + shellQuote(hash) + "\ntest -f \"$ROOT/.stella-selection-ready\"\ntest \"$(cat \"$ROOT/.stella-selection-ready\")\" = \"$HASH\"\n")
	if selectionRequestsMise(binaries, core) {
		script.WriteString("test -x \"$ROOT/bin/mise\"\n")
	}
	for _, b := range binaries {
		if safeSelectionName(b.Name) {
			script.WriteString("test -x \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
		}
	}
	for _, coreBinary := range canonicalCoreRuntimeBinaries(core) {
		if safeSelectionName(coreBinary.Name) {
			script.WriteString("test -x \"$ROOT/core/" + shellQuoteForDoubleQuotedPath(coreBinary.Name) + "\"\n")
			script.WriteString("test -x \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(coreBinary.Name) + "\"\n")
		}
	}
	return script.String()
}

func safeSelectionName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

func selectionRequestsMise(binaries []ToolBinary, core []systemplugins.RuntimeResource) bool {
	for _, b := range binaries {
		if b.Name == "mise" || b.Name == "mise.exe" || b.Tool == "mise" {
			return true
		}
	}
	for _, b := range core {
		if b.Name == "mise" || b.Name == "mise.exe" {
			return true
		}
	}
	return false
}

func cleanupToolCacheVolumes(ctx context.Context, client *dockerclient.Client, now time.Time) {
	filters := mobyclient.Filters{}.Add("label", toolCacheLabel+"=true")
	volumes, err := client.VolumeList(ctx, mobyclient.VolumeListOptions{Filters: filters})
	if err != nil {
		slog.Warn("docker selection tool cache gc: list volumes", "error", err)
		return
	}
	containers, err := client.ContainerList(ctx, mobyclient.ContainerListOptions{All: true})
	if err != nil {
		slog.Warn("docker selection tool cache gc: list containers", "error", err)
		return
	}
	for _, name := range selectStaleToolCacheVolumes(now, volumes.Items, containers.Items) {
		// Cross-process race: another stellad may have just VolumeCreate'd this
		// >7d same-hash cache and not yet ContainerCreate'd it. Removing it is
		// functionally safe because ContainerCreate recreates the named volume,
		// but that replacement is empty and unlabeled, so Docker will not GC it.
		if err := client.VolumeRemove(ctx, name, mobyclient.VolumeRemoveOptions{}); err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			slog.Warn("docker selection tool cache gc: remove volume", "volume", name, "error", err)
			continue
		}
		slog.Info("docker selection tool cache gc: removed volume", "volume", name)
	}
}

func selectStaleToolCacheVolumes(_ time.Time, _ []volume.Volume, _ []container.Summary) []string {
	// A volume that is not mounted by a currently listed container is still
	// ambiguous after a daemon or stellad crash: the durable owner reference is
	// not represented by Docker's usage count, and a creation age is not proof
	// that another peer is not between VolumeCreate and ContainerCreate. Keep
	// caches until the owner/digest reference cleanup path can prove the last
	// reference was released.
	return nil
}

func stringOption(options map[string]any, key string) (string, bool) {
	value, ok := options[key]
	if !ok {
		return "", false
	}
	s, ok := value.(string)
	return s, ok && s != ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func shellQuoteForDoubleQuotedPath(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "$", "\\$")
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}
