package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

// Tool describes a downloadable CLI tool.
type Tool struct {
	Name           string
	DisplayName    string
	Description    string
	Version        string                   // default/fallback version
	Repo           string                   // GitHub owner/repo
	AssetTemplates map[string]AssetTemplate // key: "darwin-arm64", etc.
}

// AssetTemplate describes a GitHub release asset pattern for a specific platform.
// The File field may contain "{version}" which is replaced at resolve time.
type AssetTemplate struct {
	File      string // e.g. "mise-{tag}-macos-arm64.tar.gz"
	RawBinary bool
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

// Registry is the declarative list of downloadable CLI tools.
var Registry = []Tool{
	{
		Name:        "mise",
		DisplayName: "mise",
		Description: "Polyglot runtime manager",
		Version:     "v2026.4.12",
		Repo:        "jdx/mise",
		AssetTemplates: map[string]AssetTemplate{
			"darwin-amd64":  {File: "mise-{tag}-macos-x64.tar.gz"},
			"darwin-arm64":  {File: "mise-{tag}-macos-arm64.tar.gz"},
			"linux-amd64":   {File: "mise-{tag}-linux-x64-musl.tar.gz"},
			"linux-arm64":   {File: "mise-{tag}-linux-arm64-musl.tar.gz"},
			"windows-amd64": {File: "mise-{tag}-windows-x64.zip"},
			"windows-arm64": {File: "mise-{tag}-windows-arm64.zip"},
		},
	},
	{
		Name:        "tap",
		DisplayName: "tap",
		Description: "Web content extraction tool",
		Version:     "0.4.4",
		Repo:        "vaayne/tap",
		AssetTemplates: map[string]AssetTemplate{
			"darwin-amd64":  {File: "tap_{version}_darwin_amd64.tar.gz"},
			"darwin-arm64":  {File: "tap_{version}_darwin_arm64.tar.gz"},
			"linux-amd64":   {File: "tap_{version}_linux_amd64.tar.gz"},
			"linux-arm64":   {File: "tap_{version}_linux_arm64.tar.gz"},
			"windows-amd64": {File: "tap_{version}_windows_amd64.zip"},
			"windows-arm64": {File: "tap_{version}_windows_arm64.zip"},
		},
	},
	{
		Name:        "rtk",
		DisplayName: "rtk",
		Description: "AI agent runtime toolkit",
		Version:     "0.30.0",
		Repo:        "rtk-ai/rtk",
		AssetTemplates: map[string]AssetTemplate{
			"darwin-amd64":  {File: "rtk-x86_64-apple-darwin.tar.gz"},
			"darwin-arm64":  {File: "rtk-aarch64-apple-darwin.tar.gz"},
			"linux-amd64":   {File: "rtk-x86_64-unknown-linux-musl.tar.gz"},
			"linux-arm64":   {File: "rtk-aarch64-unknown-linux-gnu.tar.gz"},
			"windows-amd64": {File: "rtk-x86_64-pc-windows-msvc.zip"},
		},
	},
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
