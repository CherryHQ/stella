//go:build darwin && arm64

package embedded

import "embed"

//go:embed binaries/darwin-arm64/*
var toolsFS embed.FS

const toolsDir = "binaries/darwin-arm64"
