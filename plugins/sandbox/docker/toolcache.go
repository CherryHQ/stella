package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sync/singleflight"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

const (
	toolCacheHelperWaitTimeout  = 5 * time.Minute
	toolCacheHelperPollInterval = 2 * time.Second
	toolCacheGCAgeThreshold     = 7 * 24 * time.Hour
)

const (
	// /opt/stella is world-traversable. /home/stella is 0700, so rootless
	// container UID 0 with all capabilities dropped cannot reach mounts there.
	// Selection artifacts are mounted over the image's /opt/stella/bin. Keeping
	// the helper's mount point separate lets it read the image binaries while
	// constructing a volume that contains only this snapshot's selection.
	containerSelectionRoot        = "/opt/stella/selection-tools"
	containerSelectionBin         = "/opt/stella/bin"
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
// Fields mirror manifest.ManifestBinary 1:1; keep them in sync.
type ToolBinary struct {
	PluginID string
	ConfigID string
	Scope    string
	Revision int64
	Name     string
	Tool     string // mise tool key: uv, bun, github:owner/repo, pipx:pkg, npm:pkg, http:name
	Version  string
	Options  map[string]any // mise tool options, using the same names as mise.toml
}

func (b ToolBinary) miseToolKey() string {
	return b.Tool
}

type selectionToolCache struct {
	VolumeName     string
	BinPath        string
	MaskVolumeName string
}

var (
	toolCacheGroup              singleflight.Group
	installSelectionToolCacheFn = installSelectionToolCache
)

// ensureSelectionToolCache prepares Linux-native artifacts for the immutable
// runner snapshot. The cache key must use the resolved image ID because a tag
// can move while a long-running stellad process is alive.
func ensureSelectionToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, imageID string) (*selectionToolCache, error) {
	if imageID == "" {
		return nil, fmt.Errorf("docker selection tool cache: resolved image ID is required")
	}
	hash := selectionToolCacheHash(imageID, cfg.SelectionToolBinaries, cfg.BundledBinarySpecs)
	volumeName := "stella-selection-" + hash[:16]
	installerName := "stella-selection-cache-" + hash[:16]
	cache := &selectionToolCache{VolumeName: volumeName, BinPath: containerSelectionBin, MaskVolumeName: "stella-selection-mask-" + hash[:16]}
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

func installSelectionToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, imageID, hash, installerName string, cache *selectionToolCache) (*selectionToolCache, error) {
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
	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image:       imageID,
		Runtime:     cfg.Runtime,
		NetworkMode: dockerclient.NetworkAllowAll,
		User:        "root",
		Env:         map[string]string{"HOME": "/root"},
		ExtraMounts: []dockerclient.Mount{{
			HostPath: cache.VolumeName, ContainerPath: containerSelectionRoot,
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
		Cwd:         containerSelectionRoot,
		Stdin:       strings.NewReader(selectionToolInstallScript(hash, cfg.SelectionToolBinaries, cfg.BundledBinarySpecs)),
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
	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image: imageID, Runtime: cfg.Runtime, NetworkMode: dockerclient.NetworkDisabled, User: "root",
		ExtraMounts: []dockerclient.Mount{{
			HostPath: cache.VolumeName, ContainerPath: containerSelectionRoot,
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
		ContainerID: containerID, Command: []string{"/bin/sh", "-s"}, Cwd: containerSelectionRoot,
		Stdin: strings.NewReader(selectionToolVerifyScript(hash, cfg.SelectionToolBinaries, cfg.BundledBinarySpecs)),
	})
	if err != nil {
		return fmt.Errorf("run selection verifier: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("selection verifier failed with exit %d", result.ExitCode)
	}
	return nil
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

func selectionToolCacheHash(imageID string, binaries, bundled []ToolBinary) string {
	return "selection-" + toolCacheHash(imageID, binaries, bundled)
}

func toolCacheHash(image string, binaries, bundled []ToolBinary) string {
	var buf bytes.Buffer
	buf.WriteString("image=")
	buf.WriteString(image)
	buf.WriteByte('\n')
	for _, b := range canonicalToolBinaries(binaries) {
		fmt.Fprintf(&buf, "%v\t%v\t%v\t%v\t%v\t%v\t", b.PluginID, b.ConfigID, b.Scope, b.Revision, b.Name, b.Tool)
		buf.WriteString(b.Version)
		buf.WriteByte('\t')
		options, _ := toml.Marshal(b.Options)
		buf.Write(options)
		buf.WriteByte('\n')
	}
	for _, b := range canonicalToolBinaries(bundled) {
		fmt.Fprintf(&buf, "bundled\t%v\t%v\t%v\t%v\t%v\n", b.PluginID, b.ConfigID, b.Scope, b.Revision, b.Name)
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

// selectionToolInstallScript runs only in the Linux helper container. It uses
// a temporary mise config and data directory, then removes both before marking
// the public volume ready. Published tools are copied as complete install
// directories so launchers can resolve adjacent libraries and other sidecars
// without consulting mise at runner time.
func selectionToolInstallScript(hash string, binaries, bundled []ToolBinary) string {
	var miseTOML string
	if len(binaries) > 0 {
		var err error
		miseTOML, err = selectionMiseTOML(binaries)
		if err != nil {
			return "echo " + shellQuote(err.Error()) + " >&2\nexit 1\n"
		}
	}
	var script strings.Builder
	script.WriteString("set -eu\n")
	script.WriteString("ROOT=" + shellQuote(containerSelectionRoot) + "\n")
	script.WriteString("PRIVATE=/tmp/stella-selection-private\n")
	script.WriteString("HASH=" + shellQuote(hash) + "\n")
	script.WriteString("trap 'rm -rf \"$PRIVATE\"' EXIT HUP INT TERM\n")
	script.WriteString("if [ -f \"$ROOT/.stella-selection-ready\" ] && [ \"$(cat \"$ROOT/.stella-selection-ready\")\" = \"$HASH\" ]; then exit 0; fi\n")
	script.WriteString("rm -rf \"$ROOT/bin\" \"$ROOT/artifacts\" \"$ROOT/.stella-selection-ready\" \"$PRIVATE\"\n")
	script.WriteString("mkdir -p \"$ROOT/bin\" \"$ROOT/artifacts\"\n")
	script.WriteString("if [ -f /opt/stella/bin/.stella-shell-env ]; then cp /opt/stella/bin/.stella-shell-env \"$ROOT/bin/.stella-shell-env\"; fi\n")
	if len(binaries) > 0 {
		script.WriteString("mkdir -p \"$PRIVATE/mise-data\" \"$PRIVATE/mise-cache\" \"$PRIVATE/mise-state\" \"$PRIVATE/mise-config\"\n")
		script.WriteString("cat > \"$PRIVATE/mise.toml\" <<'STELLA_SELECTION_MISE_TOML'\n")
		script.WriteString(miseTOML)
		if !strings.HasSuffix(miseTOML, "\n") {
			script.WriteByte('\n')
		}
		script.WriteString("STELLA_SELECTION_MISE_TOML\n")
		script.WriteString("cd \"$PRIVATE\"\n")
		miseEnv := "MISE_DATA_DIR=\"$PRIVATE/mise-data\" MISE_CACHE_DIR=\"$PRIVATE/mise-cache\" MISE_STATE_DIR=\"$PRIVATE/mise-state\" MISE_CONFIG_DIR=\"$PRIVATE/mise-config\" MISE_SYSTEM_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_GLOBAL_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_TRUSTED_CONFIG_PATHS=\"$PRIVATE\" "
		script.WriteString(miseEnv + "/opt/stella/bin/mise trust -y \"$PRIVATE/mise.toml\" >/dev/null 2>&1 || true\n")
		script.WriteString(miseEnv + "/opt/stella/bin/mise install\n")
	}
	for _, b := range binaries {
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
		artifact := shellQuoteForDoubleQuotedPath("artifact-" + b.Name)
		miseEnv := "MISE_DATA_DIR=\"$PRIVATE/mise-data\" MISE_CACHE_DIR=\"$PRIVATE/mise-cache\" MISE_STATE_DIR=\"$PRIVATE/mise-state\" MISE_CONFIG_DIR=\"$PRIVATE/mise-config\" MISE_SYSTEM_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_GLOBAL_CONFIG_FILE=\"$PRIVATE/mise.toml\" MISE_TRUSTED_CONFIG_PATHS=\"$PRIVATE\" "
		script.WriteString("install_dir=$(" + miseEnv + "/opt/stella/bin/mise where " + shellQuote(b.miseToolKey()) + ")\n")
		script.WriteString("test -d \"$install_dir\"\n")
		script.WriteString("rm -rf \"$ROOT/artifacts/" + artifact + "\"\nmkdir -p \"$ROOT/artifacts/" + artifact + "\"\ncp -R \"$install_dir/.\" \"$ROOT/artifacts/" + artifact + "/\"\n")
		script.WriteString("src=\"\"\nsrc_rel=\"\"\nif [ -f \"$ROOT/artifacts/" + artifact + "/bin/" + lookupPath + "\" ]; then src=\"$ROOT/artifacts/" + artifact + "/bin/" + lookupPath + "\"; src_rel=\"/opt/stella/selection-tools/artifacts/" + artifact + "/bin/" + lookupPath + "\"; fi\n")
		script.WriteString("if [ -z \"$src\" ] && [ -f \"$ROOT/artifacts/" + artifact + "/" + lookupPath + "\" ]; then src=\"$ROOT/artifacts/" + artifact + "/" + lookupPath + "\"; src_rel=\"/opt/stella/selection-tools/artifacts/" + artifact + "/" + lookupPath + "\"; fi\n")
		script.WriteString("test -n \"$src\" && test -x \"$src\"\nln -s \"$src_rel\" \"$ROOT/bin/" + name + "\"\n")
	}
	for _, bundledBinary := range canonicalToolBinaries(bundled) {
		name := bundledBinary.Name
		if !safeSelectionName(name) {
			script.WriteString("echo " + shellQuote("invalid bundled binary name "+name) + " >&2\nexit 1\n")
			continue
		}
		quoted := shellQuoteForDoubleQuotedPath(name)
		artifact := shellQuoteForDoubleQuotedPath("bundled-" + name)
		script.WriteString("test -x /opt/stella/bin/" + quoted + "\n")
		script.WriteString("source=$(readlink -f /opt/stella/bin/" + quoted + ")\n")
		script.WriteString("test -n \"$source\"\nrm -rf \"$ROOT/artifacts/" + artifact + "\"\nmkdir -p \"$ROOT/artifacts/" + artifact + "\"\n")
		// Xberg is a launcher symlink into a versioned directory. Copy that
		// directory, while a single-file bundled asset stays single-file and
		// cannot accidentally copy the whole image bin directory.
		script.WriteString("source_dir=$(dirname \"$source\")\nif [ \"$source_dir\" = /opt/stella/bin ]; then cp \"$source\" \"$ROOT/artifacts/" + artifact + "/" + quoted + "\"; else cp -R \"$source_dir/.\" \"$ROOT/artifacts/" + artifact + "/\"; fi\n")
		script.WriteString("test -x \"$ROOT/artifacts/" + artifact + "/" + quoted + "\"\nln -s \"/opt/stella/selection-tools/artifacts/" + artifact + "/" + quoted + "\" \"$ROOT/bin/" + quoted + "\"\n")
	}
	// The trap removes the private installer layer even when any command above fails.
	script.WriteString("printf '%s' \"$HASH\" > \"$ROOT/.stella-selection-ready\"\nchmod 0444 \"$ROOT/.stella-selection-ready\"\n")
	return script.String()
}

func selectionToolVerifyScript(hash string, binaries, bundled []ToolBinary) string {
	var script strings.Builder
	script.WriteString("set -eu\nROOT=" + shellQuote(containerSelectionRoot) + "\nHASH=" + shellQuote(hash) + "\ntest -f \"$ROOT/.stella-selection-ready\"\ntest \"$(cat \"$ROOT/.stella-selection-ready\")\" = \"$HASH\"\n")
	if selectionRequestsMise(binaries, bundled) {
		script.WriteString("test -x \"$ROOT/bin/mise\"\n")
	}
	for _, b := range binaries {
		if safeSelectionName(b.Name) {
			script.WriteString("test -x \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
		}
	}
	for _, bundledBinary := range canonicalToolBinaries(bundled) {
		name := bundledBinary.Name
		if safeSelectionName(name) {
			script.WriteString("test -x \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(name) + "\"\n")
		}
	}
	return script.String()
}

func safeSelectionName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

func selectionRequestsMise(binaries, bundled []ToolBinary) bool {
	for _, b := range append(slices.Clone(binaries), bundled...) {
		if b.Name == "mise" || b.Name == "mise.exe" || b.Tool == "mise" {
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

func selectStaleToolCacheVolumes(now time.Time, volumes []volume.Volume, containers []container.Summary) []string {
	used := referencedVolumeNames(containers)
	var selected []string
	for _, v := range volumes {
		if v.Labels[toolCacheLabel] != "true" {
			continue
		}
		if _, ok := used[v.Name]; ok {
			continue
		}
		if v.UsageData != nil && v.UsageData.RefCount > 0 {
			continue
		}
		createdAt := v.Labels[toolCacheCreatedAtLabel]
		if createdAt == "" {
			continue
		}
		created, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			continue
		}
		if now.Sub(created) <= toolCacheGCAgeThreshold {
			continue
		}
		selected = append(selected, v.Name)
	}
	return selected
}

func referencedVolumeNames(containers []container.Summary) map[string]struct{} {
	used := map[string]struct{}{}
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type != mount.TypeVolume {
				continue
			}
			if m.Name != "" {
				used[m.Name] = struct{}{}
			}
		}
	}
	return used
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
