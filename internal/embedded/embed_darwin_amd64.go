//go:build darwin && amd64

package embedded

import "embed"

//go:embed binaries/darwin-amd64/*
var toolsFS embed.FS

const toolsDir = "binaries/darwin-amd64"
