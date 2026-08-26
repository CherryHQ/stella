//go:build darwin && amd64

package binaries

import "embed"

//go:embed binaries/darwin-amd64/mise.gz binaries/darwin-amd64/xberg.tar.gz
var toolsFS embed.FS

const toolsDir = "binaries/darwin-amd64"
