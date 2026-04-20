//go:build linux && arm64

package binaries

import "embed"

//go:embed binaries/linux-arm64/*
var toolsFS embed.FS

const toolsDir = "binaries/linux-arm64"
