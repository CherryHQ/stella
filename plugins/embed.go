package plugins

import "embed"

//go:embed tool/*/plugin.json channel/*/plugin.json
var FS embed.FS
