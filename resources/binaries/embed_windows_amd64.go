//go:build windows && amd64

package binaries

import "embed"

//go:embed binaries/windows-amd64/mise.exe.gz
var toolsFS embed.FS

const toolsDir = "binaries/windows-amd64"
