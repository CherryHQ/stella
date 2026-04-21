//go:build windows && arm64

package binaries

import "embed"

//go:embed binaries/windows-arm64/*
var toolsFS embed.FS

const toolsDir = "binaries/windows-arm64"
