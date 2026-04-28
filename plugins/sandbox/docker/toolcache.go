package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
	"github.com/pelletier/go-toml/v2"
	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

const (
	toolCacheHelperWaitTimeout  = 5 * time.Minute
	toolCacheHelperPollInterval = 2 * time.Second
)

const (
	containerUserToolsRoot = "/home/anna/.anna-tools"
	containerUserToolsBin  = containerUserToolsRoot + "/bin"
)

// ToolBinary describes a user-configured CLI that must be installed in a Linux
// container context before docker sandbox sessions can execute it.
// Fields mirror manifestplugins.ManifestBinary 1:1; keep them in sync.
type ToolBinary struct {
	Name    string
	Tool    string // mise tool key: github:owner/repo, pipx:pkg, npm:pkg, http:name
	URL     string // required for http backend
	Version string

	// Shared asset options (github + http)
	StripComponents int
	BinPath         string
	Bin             string
	RenameExe       string
	Checksum        string

	// GitHub-only
	AssetPattern  string
	VersionPrefix string
	NoApp         bool
	FilterBins    string
	Prerelease    bool
	APIURL        string

	// HTTP-only
	Size            string
	Format          string
	VersionListURL  string
	VersionRegex    string
	VersionJSONPath string
	VersionExpr     string

	// Pipx-only
	Extras   string
	PipxArgs string
	UVX      bool
	UVXArgs  string
}

func (b ToolBinary) resolvedBackend() string {
	if idx := strings.IndexByte(b.Tool, ':'); idx >= 0 {
		return b.Tool[:idx]
	}
	return ""
}

func (b ToolBinary) miseToolKey() string {
	return b.Tool
}

type userToolCache struct {
	VolumeName string
	BinPath    string
}

func ensureUserToolCache(ctx context.Context, client *dockerclient.Client, cfg Config) (*userToolCache, error) {
	if len(cfg.UserToolBinaries) == 0 {
		return nil, nil
	}

	hash := userToolCacheHash(cfg.Image, cfg.UserToolBinaries)
	volumeName := "anna-tools-" + hash[:16]
	installerName := "anna-tool-cache-" + hash[:16]
	cache := &userToolCache{VolumeName: volumeName, BinPath: containerUserToolsBin}

	if _, err := client.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Name: volumeName,
		Labels: map[string]string{
			"anna.tool_cache": "true",
			"anna.image":      cfg.Image,
			"anna.hash":       hash,
		},
	}); err != nil {
		return nil, fmt.Errorf("docker user tool cache: create volume %s: %w", volumeName, err)
	}

	containerID, err := client.CreateAndStart(ctx, dockerclient.CreateOptions{
		Image:       cfg.Image,
		NetworkMode: dockerclient.NetworkAllowAll,
		User:        "root",
		Env: map[string]string{
			"HOME": "/root",
		},
		ReadOnlyMounts: []dockerclient.Mount{
			{
				HostPath:      volumeName,
				ContainerPath: containerUserToolsRoot,
				ReadOnly:      false,
				Type:          dockerclient.MountTypeVolume,
			},
		},
		Labels: map[string]string{
			"anna.tool_cache_helper": "true",
			"anna.tool_cache":        volumeName,
		},
		Name: installerName,
	})
	if err != nil {
		if errdefs.IsConflict(err) {
			// Another app instance is already running the installer for this
			// tool set. Wait for it to finish instead of racing.
			return waitForToolCache(ctx, client, installerName, cache)
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
func waitForToolCache(ctx context.Context, client *dockerclient.Client, installerName string, cache *userToolCache) (*userToolCache, error) {
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
			// Installer finished and was already cleaned up — volume is ready.
			return cache, nil
		}
		if !state.Running {
			if stopErr := client.Stop(context.Background(), installerName); stopErr != nil {
				slog.Warn("docker user tool cache: cleanup stopped installer", "name", installerName, "error", stopErr)
			}
			if state.ExitCode != 0 {
				return nil, fmt.Errorf("docker user tool cache: installer %s exited with %d", installerName, state.ExitCode)
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

func userToolCacheHash(image string, binaries []ToolBinary) string {
	var buf bytes.Buffer
	buf.WriteString("image=")
	buf.WriteString(image)
	buf.WriteByte('\n')
	for _, b := range binaries {
		// Include all identity and option fields so any change busts the cache.
		fmt.Fprintf(&buf, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			b.Name, b.Tool, b.URL, b.Version,
			b.Checksum, b.StripComponents, b.BinPath, b.Bin, b.RenameExe,
			b.AssetPattern, b.VersionPrefix, b.FilterBins, b.NoApp, b.APIURL,
			b.Size, b.Format, b.VersionListURL, b.VersionRegex, b.VersionJSONPath, b.VersionExpr,
			b.UVX, b.Extras, b.PipxArgs, b.UVXArgs,
		)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func toolBinarySharedOptions(b ToolBinary, m map[string]any) {
	if b.StripComponents > 0 {
		m["strip_components"] = b.StripComponents
	}
	if b.BinPath != "" {
		m["bin_path"] = b.BinPath
	}
	if b.Bin != "" {
		m["bin"] = b.Bin
	}
	if b.RenameExe != "" {
		m["rename_exe"] = b.RenameExe
	}
	if b.Checksum != "" {
		m["checksum"] = b.Checksum
	}
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

		var toolValue any
		switch b.resolvedBackend() {
		case "github":
			m := map[string]any{"version": ver}
			if b.AssetPattern != "" {
				m["asset_pattern"] = b.AssetPattern
			}
			if b.VersionPrefix != "" {
				m["version_prefix"] = b.VersionPrefix
			}
			if b.NoApp {
				m["no_app"] = true
			}
			if b.FilterBins != "" {
				m["filter_bins"] = b.FilterBins
			}
			if b.Prerelease {
				m["prerelease"] = true
			}
			if b.APIURL != "" {
				m["api_url"] = b.APIURL
			}
			toolBinarySharedOptions(b, m)
			if len(m) == 1 {
				toolValue = ver
			} else {
				toolValue = m
			}

		case "http":
			m := map[string]any{"version": ver, "url": b.URL}
			if b.Size != "" {
				m["size"] = b.Size
			}
			if b.Format != "" {
				m["format"] = b.Format
			}
			if b.VersionListURL != "" {
				m["version_list_url"] = b.VersionListURL
			}
			if b.VersionRegex != "" {
				m["version_regex"] = b.VersionRegex
			}
			if b.VersionJSONPath != "" {
				m["version_json_path"] = b.VersionJSONPath
			}
			if b.VersionExpr != "" {
				m["version_expr"] = b.VersionExpr
			}
			toolBinarySharedOptions(b, m)
			toolValue = m

		case "pipx":
			m := map[string]any{"version": ver}
			if b.Extras != "" {
				m["extras"] = b.Extras
			}
			if b.PipxArgs != "" {
				m["pipx_args"] = b.PipxArgs
			}
			if b.UVX {
				m["uvx"] = true
			}
			if b.UVXArgs != "" {
				m["uvx_args"] = b.UVXArgs
			}
			if len(m) == 1 {
				toolValue = ver
			} else {
				toolValue = m
			}

		case "npm":
			toolValue = ver

		default:
			return "", fmt.Errorf("binary %q: unsupported backend %q", b.Name, b.resolvedBackend())
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
	script.WriteString("if [ -f \"$ROOT/.anna-tools-ready\" ] && [ \"$(cat \"$ROOT/.anna-tools-ready\")\" = \"$HASH\" ]; then exit 0; fi\n")
	script.WriteString("rm -rf \"$ROOT/bin\" \"$ROOT/mise-data\" \"$ROOT/mise.toml\" \"$ROOT/.anna-tools-ready\"\n")
	script.WriteString("mkdir -p \"$ROOT/bin\" \"$ROOT/mise-data\"\n")
	script.WriteString("cat > \"$ROOT/mise.toml\" <<'ANNA_MISE_TOML'\n")
	script.WriteString(miseTOML)
	if !strings.HasSuffix(miseTOML, "\n") {
		script.WriteByte('\n')
	}
	script.WriteString("ANNA_MISE_TOML\n")
	script.WriteString("cd \"$ROOT\"\n")
	script.WriteString("MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise trust -y \"$ROOT/mise.toml\" >/dev/null 2>&1 || true\n")
	script.WriteString("MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise install\n")
	for _, b := range binaries {
		lookup := b.Name
		if b.RenameExe != "" {
			lookup = b.RenameExe
		} else if b.Bin != "" {
			lookup = b.Bin
		}
		script.WriteString("install_dir=$(MISE_DATA_DIR=\"$ROOT/mise-data\" MISE_TRUSTED_CONFIG_PATHS=\"$ROOT\" mise where " + shellQuote(b.miseToolKey()) + ")\n")
		script.WriteString("src=\"\"\n")
		script.WriteString("if [ -f \"$install_dir/bin/" + shellQuoteForDoubleQuotedPath(lookup) + "\" ]; then src=\"$install_dir/bin/" + shellQuoteForDoubleQuotedPath(lookup) + "\"; fi\n")
		script.WriteString("if [ -z \"$src\" ] && [ -f \"$install_dir/" + shellQuoteForDoubleQuotedPath(lookup) + "\" ]; then src=\"$install_dir/" + shellQuoteForDoubleQuotedPath(lookup) + "\"; fi\n")
		script.WriteString("if [ -z \"$src\" ]; then echo " + shellQuote("binary "+lookup+" not found for "+b.Tool) + " >&2; exit 1; fi\n")
		script.WriteString("cp \"$src\" \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
		script.WriteString("chmod 0755 \"$ROOT/bin/" + shellQuoteForDoubleQuotedPath(b.Name) + "\"\n")
	}
	script.WriteString("printf '%s' \"$HASH\" > \"$ROOT/.anna-tools-ready\"\n")
	return script.String()
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
