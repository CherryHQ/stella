//go:build !stella_embed_pg && darwin && arm64

package pgbundle

import "embed"

//go:embed bundles/darwin-arm64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/darwin-arm64"
