//go:build !stella_embed_pg && windows && arm64

package pgbundle

import "embed"

//go:embed bundles/windows-arm64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/windows-arm64"
