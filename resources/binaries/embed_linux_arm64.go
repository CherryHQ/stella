//go:build linux && arm64

package binaries

import "embed"

//go:embed binaries/linux-arm64/mise.gz binaries/linux-arm64/xberg.tar.gz
var toolsFS embed.FS

const toolsDir = "binaries/linux-arm64"
