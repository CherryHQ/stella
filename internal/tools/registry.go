package tools

import "runtime"

// Tool describes a downloadable CLI tool.
type Tool struct {
	Name        string
	DisplayName string
	Description string
	Version     string
	Repo        string           // GitHub owner/repo
	Assets      map[string]Asset // key: "darwin-arm64", "linux-amd64", etc.
}

// Asset describes a GitHub release asset for a specific platform.
type Asset struct {
	Tag       string // release tag, e.g. "v2026.4.12"
	File      string // asset filename
	RawBinary bool   // true if asset is a raw binary (not an archive)
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
		Assets: map[string]Asset{
			"darwin-amd64":  {Tag: "v2026.4.12", File: "mise-v2026.4.12-macos-x64.tar.gz"},
			"darwin-arm64":  {Tag: "v2026.4.12", File: "mise-v2026.4.12-macos-arm64.tar.gz"},
			"linux-amd64":   {Tag: "v2026.4.12", File: "mise-v2026.4.12-linux-x64-musl.tar.gz"},
			"linux-arm64":   {Tag: "v2026.4.12", File: "mise-v2026.4.12-linux-arm64-musl.tar.gz"},
			"windows-amd64": {Tag: "v2026.4.12", File: "mise-v2026.4.12-windows-x64.zip"},
			"windows-arm64": {Tag: "v2026.4.12", File: "mise-v2026.4.12-windows-arm64.zip"},
		},
	},
	{
		Name:        "tap",
		DisplayName: "tap",
		Description: "Web content extraction tool",
		Version:     "0.4.4",
		Repo:        "vaayne/tap",
		Assets: map[string]Asset{
			"darwin-amd64":  {Tag: "v0.4.4", File: "tap_0.4.4_darwin_amd64.tar.gz"},
			"darwin-arm64":  {Tag: "v0.4.4", File: "tap_0.4.4_darwin_arm64.tar.gz"},
			"linux-amd64":   {Tag: "v0.4.4", File: "tap_0.4.4_linux_amd64.tar.gz"},
			"linux-arm64":   {Tag: "v0.4.4", File: "tap_0.4.4_linux_arm64.tar.gz"},
			"windows-amd64": {Tag: "v0.4.4", File: "tap_0.4.4_windows_amd64.zip"},
			"windows-arm64": {Tag: "v0.4.4", File: "tap_0.4.4_windows_arm64.zip"},
		},
	},
	{
		Name:        "rtk",
		DisplayName: "rtk",
		Description: "AI agent runtime toolkit",
		Version:     "0.30.0",
		Repo:        "rtk-ai/rtk",
		Assets: map[string]Asset{
			"darwin-amd64":  {Tag: "v0.30.0", File: "rtk-x86_64-apple-darwin.tar.gz"},
			"darwin-arm64":  {Tag: "v0.30.0", File: "rtk-aarch64-apple-darwin.tar.gz"},
			"linux-amd64":   {Tag: "v0.30.0", File: "rtk-x86_64-unknown-linux-musl.tar.gz"},
			"linux-arm64":   {Tag: "v0.30.0", File: "rtk-aarch64-unknown-linux-gnu.tar.gz"},
			"windows-amd64": {Tag: "v0.30.0", File: "rtk-x86_64-pc-windows-msvc.zip"},
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
