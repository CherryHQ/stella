//go:build linux && arm64

package embedded

import "embed"

//go:embed binaries/linux-arm64/*
var toolsFS embed.FS

const toolsDir = "binaries/linux-arm64"
