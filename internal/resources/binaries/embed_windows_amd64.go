//go:build windows && amd64

package binaries

import "embed"

//go:embed binaries/windows-amd64/*
var toolsFS embed.FS

const toolsDir = "binaries/windows-amd64"
