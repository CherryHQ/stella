//go:build darwin && arm64

package binaries

import "embed"

//go:embed binaries/darwin-arm64/mise.gz binaries/darwin-arm64/xberg.tar.gz
var toolsFS embed.FS

const toolsDir = "binaries/darwin-arm64"
