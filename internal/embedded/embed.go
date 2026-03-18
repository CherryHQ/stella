package embedded

import "embed"

//go:embed binaries/*
var toolsFS embed.FS

const toolsDir = "binaries"
