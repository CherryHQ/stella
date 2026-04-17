package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

//go:embed registry.json
var registryJSON []byte

// Tool describes a downloadable CLI tool.
type Tool struct {
	Name           string                   `json:"name"`
	DisplayName    string                   `json:"display_name"`
	Description    string                   `json:"description"`
	Version        string                   `json:"version"`
	Repo           string                   `json:"repo"`
	AssetTemplates map[string]AssetTemplate `json:"asset_templates"`
}

// AssetTemplate describes a GitHub release asset pattern for a specific platform.
// The File field may contain "{version}" which is replaced at resolve time.
type AssetTemplate struct {
	File      string `json:"file"`
	RawBinary bool   `json:"raw_binary,omitempty"`
}

// Asset is a resolved, ready-to-download asset.
type Asset struct {
	Tag       string
	File      string
	RawBinary bool
}

// ResolveAsset resolves the asset template for a given platform and version.
func (t *Tool) ResolveAsset(platform, version string) (Asset, bool) {
	tmpl, ok := t.AssetTemplates[platform]
	if !ok {
		return Asset{}, false
	}
	tag := ensureVPrefix(version)
	file := strings.ReplaceAll(tmpl.File, "{version}", version)
	file = strings.ReplaceAll(file, "{tag}", tag)
	return Asset{
		Tag:       tag,
		File:      file,
		RawBinary: tmpl.RawBinary,
	}, true
}

func ensureVPrefix(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// Platform returns "GOOS-GOARCH" for the current runtime.
func Platform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// Registry is the declarative list of downloadable CLI tools, loaded from registry.json.
var Registry []Tool

func init() {
	if err := json.Unmarshal(registryJSON, &Registry); err != nil {
		panic("tools: invalid registry.json: " + err.Error())
	}
}

// FindTool returns a pointer to the named tool in the Registry, or nil.
func FindTool(name string) *Tool {
	for i := range Registry {
		if Registry[i].Name == name {
			return &Registry[i]
		}
	}
	return nil
}

// githubRelease is the GitHub API response for a release.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// FetchLatestVersion queries the GitHub API for the latest release tag.
func FetchLatestVersion(ctx context.Context, tool *Tool) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", tool.Repo)
	var release githubRelease
	resp, err := httpClient.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetResult(&release).
		Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch latest release for %s: %w", tool.Name, err)
	}
	if resp.StatusCode() != 200 {
		return "", fmt.Errorf("fetch latest release for %s: status %d", tool.Name, resp.StatusCode())
	}
	if release.TagName == "" {
		return "", fmt.Errorf("fetch latest release for %s: empty tag", tool.Name)
	}
	return strings.TrimPrefix(release.TagName, "v"), nil
}
