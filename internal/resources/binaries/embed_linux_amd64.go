//go:build linux && amd64

package binaries

import "embed"

//go:embed binaries/linux-amd64/*
var toolsFS embed.FS

const toolsDir = "binaries/linux-amd64"
