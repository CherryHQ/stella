package embedded

import "embed"

//go:embed binaries/*
var toolsFS embed.FS

const toolsDir = "binaries"

//go:embed plugins/*
var pluginsFS embed.FS

const pluginsDir = "plugins"
