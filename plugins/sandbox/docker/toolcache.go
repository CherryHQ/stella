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
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sync/singleflight"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
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
	containerUserToolsRoot        = "/opt/stella/user-tools"
	containerUserToolsBin         = containerUserToolsRoot + "/bin"
	containerUserToolsReadyMarker = containerUserToolsRoot + "/.stella-tools-ready"
)

const (
	toolCacheLabel          = "stella.tool_cache"
	toolCacheImageLabel     = "stella.image"
	toolCacheHashLabel      = "stella.hash"
	toolCacheCreatedAtLabel = "stella.tool_cache.created_at"
)

// ToolBinary describes a user-configured CLI that must be installed in a Linux
// container context before docker sandbox sessions can execute it.
// Fields mirror manifest.ManifestBinary 1:1; keep them in sync.
type ToolBinary struct {
	Name    string
	Tool    string // mise tool key: uv, bun, github:owner/repo, pipx:pkg, npm:pkg, http:name
	Version string
	Options map[string]any // mise tool options, using the same names as mise.toml
}

func (b ToolBinary) miseToolKey() string {
	return b.Tool
}

type userToolCache struct {
	VolumeName string
	BinPath    string
}

var (
	toolCacheMu        sync.Mutex
	toolCacheReady     = map[string]*userToolCache{}
	toolCacheGroup     singleflight.Group
	installToolCacheFn = installUserToolCache
)

func ensureUserToolCache(ctx context.Context, client *dockerclient.Client, cfg Config) (*userToolCache, error) {
	if len(cfg.UserToolBinaries) == 0 {
		return nil, nil
	}

	hash := userToolCacheHash(cfg.Image, cfg.UserToolBinaries)
	volumeName := "stella-tools-" + hash[:16]
	installerName := "stella-tool-cache-" + hash[:16]
	cache := &userToolCache{VolumeName: volumeName, BinPath: containerUserToolsBin}

	if ready := cachedToolCache(hash); ready != nil {
		return ready, nil
	}

	value, err, _ := toolCacheGroup.Do(hash, func() (any, error) {
		if ready := cachedToolCache(hash); ready != nil {
			return ready, nil
		}
		ready, err := installToolCacheFn(ctx, client, cfg, hash, installerName, cache)
		if err != nil {
			return nil, err
		}
		markToolCacheReady(hash, ready)
		return ready, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*userToolCache), nil
}

func installUserToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, hash, installerName string, cache *userToolCache) (*userToolCache, error) {
	if _, err := client.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Name: cache.VolumeName,
		Labels: map[string]string{
			toolCacheLabel:          "true",
			toolCacheImageLabel:     cfg.Image,
			toolCacheHashLabel:      hash,
			toolCacheCreatedAtLabel: time.Now().UTC().Format(time.RFC3339),
		},
	}); err != nil {
		return nil, fmt.Errorf("docker user tool cache: create volume %s: %w", cache.VolumeName, err)
	}

	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image:       cfg.Image,
		Runtime:     cfg.Runtime,
		NetworkMode: dockerclient.NetworkAllowAll,
		User:        "root",
		Env: map[string]string{
			"HOME": "/root",
		},
		ExtraMounts: []dockerclient.Mount{
			{
				HostPath:      cache.VolumeName,
				ContainerPath: containerUserToolsRoot,
				ReadOnly:      false,
				Type:          dockerclient.MountTypeVolume,
			},
		},
		Labels: map[string]string{
			"stella.tool_cache_helper": "true",
			toolCacheLabel:             cache.VolumeName,
		},
		Name: installerName,
	})
	if err != nil {
		if errdefs.IsConflict(err) {
			// Another app instance is already running the installer for this
			// tool set. Wait for it to finish instead of racing.
			return waitForToolCache(ctx, client, installerName, cache, func(ctx context.Context) error {
				return verifyUserToolCache(ctx, client, cfg, hash, cache)
			})
		}
		return nil, fmt.Errorf("docker user tool cache: start helper: %w", err)
	}
	defer func() {
		if stopErr := client.Stop(context.Background(), containerID); stopErr != nil {
			slog.Warn("docker user tool cache helper cleanup failed", "container_id", containerID, "error", stopErr)
		}
	}()

	result, err := client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: containerID,
		Command:     []string{"/bin/sh", "-s"},
		Cwd:         containerUserToolsRoot,
		Stdin:       strings.NewReader(userToolInstallScript(hash, cfg.UserToolBinaries)),
	})
	if err != nil {
		return nil, fmt.Errorf("docker user tool cache: run installer: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("docker user tool cache: installer failed with exit %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
	}

	return cache, nil
}

// waitForToolCache waits for a concurrently running installer container to
// finish and returns the cache if it succeeded. Used when another app instance
// already holds the installer container name (the distributed mutex).
func waitForToolCache(ctx context.Context, client *dockerclient.Client, installerName string, cache *userToolCache, verify func(context.Context) error) (*userToolCache, error) {
	deadline := time.Now().Add(toolCacheHelperWaitTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("docker user tool cache: timed out waiting for installer %s", installerName)
		}

		state, err := client.InspectContainerState(ctx, installerName)
		if err != nil {
			return nil, fmt.Errorf("docker user tool cache: inspect installer: %w", err)
		}
		if state == nil {
			// The helper disappearing is ambiguous: Docker removes it after both
			// success and failure, so fail closed unless the volume proves ready.
			if err := verify(ctx); err != nil {
				return nil, fmt.Errorf("docker user tool cache: installer %s finished but cache is not ready: %w", installerName, err)
			}
			return cache, nil
		}
		if !state.Running {
			if stopErr := client.Stop(context.Background(), installerName); stopErr != nil {
				slog.Warn("docker user tool cache: cleanup stopped installer", "name", installerName, "error", stopErr)
			}
			if state.ExitCode != 0 {
				return nil, fmt.Errorf("docker user tool cache: installer %s exited with %d", installerName, state.ExitCode)
			}
			if err := verify(ctx); err != nil {
				return nil, fmt.Errorf("docker user tool cache: installer %s exited successfully but cache is not ready: %w", installerName, err)
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

func cachedToolCache(hash string) *userToolCache {
	toolCacheMu.Lock()
	defer toolCacheMu.Unlock()
	return toolCacheReady[hash]
}

func markToolCacheReady(hash string, cache *userToolCache) {
	toolCacheMu.Lock()
	defer toolCacheMu.Unlock()
	toolCacheReady[hash] = cache
}

func resetToolCacheMemoForTest() {
	toolCacheMu.Lock()
	defer toolCacheMu.Unlock()
	toolCacheReady = map[string]*userToolCache{}
	toolCacheGroup = singleflight.Group{}
	installToolCacheFn = installUserToolCache
}

func verifyUserToolCache(ctx context.Context, client *dockerclient.Client, cfg Config, hash string, cache *userToolCache) error {
	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image:       cfg.Image,
		Runtime:     cfg.Runtime,
		NetworkMode: dockerclient.NetworkDisabled,
		User:        "root",
		ExtraMounts: []dockerclient.Mount{
			{
				HostPath:      cache.VolumeName,
				ContainerPath: containerUserToolsRoot,
				ReadOnly:      true,
				Type:          dockerclient.MountTypeVolume,
			},
		},
		Labels: map[string]string{
			"stella.tool_cache_verifier": "true",
			toolCacheLabel:               cache.VolumeName,
		},
	})
	if err != nil {
		return fmt.Errorf("start verifier: %w", err)
	}
	defer func() {
		if stopErr := client.Stop(context.Background(), containerID); stopErr != nil {
			slog.Warn("docker user tool cache verifier cleanup failed", "container_id", containerID, "error", stopErr)
		}
	}()

	result, err := client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: containerID,
		Command:     []string{"/bin/sh", "-s"},
		Cwd:         containerUserToolsRoot,
		Stdin:       strings.NewReader(userToolVerifyScript(hash, cfg.UserToolBinaries)),
	})
	if err != nil {
		return fmt.Errorf("run verifier: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("verifier failed with exit %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}

func userToolCacheHash(image string, binaries []ToolBinary) string {
	var buf bytes.Buffer
	buf.WriteString("image=")
	buf.WriteString(image)
	buf.WriteByte('\n')
	for _, b := range binaries {
		fmt.Fprintf(&buf, "%v\t%v\t%v\t", b.Name, b.Tool, b.Version)
		options, _ := toml.Marshal(b.Options)
		buf.Write(options)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func userToolsMiseTOML(binaries []ToolBinary) (string, error) {
	tools := make(map[string]any, len(binaries))
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

func userToolInstallScript(hash string, binaries []ToolBinary) string {
	miseTOML, err := userToolsMiseTOML(binaries)
	if err != nil {
		// userToolCacheHash currently validates nothing, and TOML marshal for this
		// shape should not fail. Surface the error in-shell if that ever changes.
		return "echo " + shellQuote(err.Error()) + " >&2\nexit 1\n"
	}

	var script strings.Builder
	script.WriteString("set -eu\n")
	script.WriteString("ROOT=" + shellQuote(containerUserToolsRoot) + "\n")
	script.WriteString("HASH=" + shellQuote(hash) + "\n")
	script.WriteString("if [ -f \"$ROOT/.stella-tools-ready\" ] && [ \"$(cat \"$ROOT/.stella-tools-ready\")\" = \"$HASH\" ]; then exit 0; fi\n")
	script.WriteString("rm -rf \"$ROOT/bin\" \"$ROOT/mise-data\" \"$ROOT/mise.toml\" \"$ROOT/.stella-tools-ready\"\n")
	script.WriteString("mkdir -p \"$ROOT/bin\" \"$ROOT/mise-data\"\n")
	script.WriteString("cat > \"$ROOT/mise.toml\" <<'STELLA_MISE_TOML'\n")
	script.WriteString(miseTOML)
	if !strings.HasSuffix(miseTOML, "\n") {
		script.WriteByte('\n')
	}
	script.WriteString("STELLA_MISE_TOML\n")
	script.WriteString("cd \"$ROOT\"\n")
	script.WriteString("MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise trust -y \"$ROOT/mise.toml\" >/dev/null 2>&1 || true\n")
	script.WriteString("MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise install\n")
	for _, b := range binaries {
		lookup := b.Name
		if renameExe, ok := stringOption(b.Options, "rename_exe"); ok {
			lookup = renameExe
		} else if bin, ok := stringOption(b.Options, "bin"); ok {
			lookup = bin
		}
		script.WriteString("install_dir=$(MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise where " + shellQuote(b.miseToolKey()) + ")\n")
		script.WriteString("src=\"\"\n")
		script.WriteString("if [ -f \"$install_dir/bin/" + shellQuoteForDoubleQuotedPath(lookup) + "\" ]; then src=\"$install_dir/bin/" + shellQuoteForDoubleQuotedPath(lookup) + "\"; fi\n")
		script.WriteString("if [ -z \"$src\" ] && [ -f \"$install_dir/" + shellQuoteForDoubleQuotedPath(lookup) + "\" ]; then src=\"$install_dir/" + shellQuoteForDoubleQuotedPath(lookup) + "\"; fi\n")
		script.WriteString("if [ -z \"$src\" ]; then echo " + shellQuote("binary "+lookup+" not found for "+b.Tool) + " >&2; exit 1; fi\n")
		script.WriteString("cp \"$src\" \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
		script.WriteString("chmod 0755 \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
	}
	script.WriteString("printf '%s' \"$HASH\" > \"$ROOT/.stella-tools-ready\"\n")
	return script.String()
}

func userToolVerifyScript(hash string, binaries []ToolBinary) string {
	var script strings.Builder
	script.WriteString("set -eu\n")
	script.WriteString("ROOT=" + shellQuote(containerUserToolsRoot) + "\n")
	script.WriteString("HASH=" + shellQuote(hash) + "\n")
	script.WriteString("test -f " + shellQuote(containerUserToolsReadyMarker) + "\n")
	script.WriteString("test \"$(cat " + shellQuote(containerUserToolsReadyMarker) + ")\" = \"$HASH\"\n")
	script.WriteString("test -d \"$ROOT/bin\"\n")
	for _, b := range binaries {
		script.WriteString("test -x \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
	}
	return script.String()
}

func cleanupToolCacheVolumes(ctx context.Context, client *dockerclient.Client, now time.Time) {
	filters := mobyclient.Filters{}.Add("label", toolCacheLabel+"=true")
	volumes, err := client.VolumeList(ctx, mobyclient.VolumeListOptions{Filters: filters})
	if err != nil {
		slog.Warn("docker user tool cache gc: list volumes", "error", err)
		return
	}
	containers, err := client.ContainerList(ctx, mobyclient.ContainerListOptions{All: true})
	if err != nil {
		slog.Warn("docker user tool cache gc: list containers", "error", err)
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
			slog.Warn("docker user tool cache gc: remove volume", "volume", name, "error", err)
			continue
		}
		slog.Info("docker user tool cache gc: removed volume", "volume", name)
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

// resolveUserToolBinaries loads manifest plugins and returns user-configured
// tool binaries that differ from builtins. Called by NewFactory when
// StellaHome is set. Sandbox tool binaries derive from manifest defaults
// only — overrides cannot change which binaries ship in the image.
func resolveUserToolBinaries() ([]ToolBinary, error) {
	builtin, err := manifest.LoadBuiltin()
	if err != nil {
		return nil, err
	}

	builtinByID := make(map[string]manifest.ManifestPlugin, len(builtin.Plugins))
	for _, plugin := range builtin.Plugins {
		builtinByID[plugin.ID] = plugin
	}

	var out []ToolBinary
	for _, plugin := range builtin.Plugins {
		if !plugin.Enabled || len(plugin.Binaries) == 0 {
			continue
		}
		if builtinPlugin, ok := builtinByID[plugin.ID]; ok && reflect.DeepEqual(plugin.Binaries, builtinPlugin.Binaries) {
			continue
		}
		for _, binary := range plugin.Binaries {
			out = append(out, ToolBinary{
				Name:    binary.Name,
				Tool:    binary.Tool,
				Version: binary.Version,
				Options: binary.Options,
			})
		}
	}
	return out, nil
}
