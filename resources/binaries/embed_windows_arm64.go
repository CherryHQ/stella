//go:build windows && arm64

package binaries

import "embed"

//go:embed binaries/windows-arm64/mise.exe.gz
var toolsFS embed.FS

const toolsDir = "binaries/windows-arm64"
