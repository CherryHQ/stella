//go:build !stella_embed_pg && windows && amd64

package pgbundle

import "embed"

//go:embed bundles/windows-amd64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/windows-amd64"
