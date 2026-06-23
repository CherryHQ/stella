//go:build !stella_embed_pg && linux && arm64

package pgbundle

import "embed"

//go:embed bundles/linux-arm64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/linux-arm64"
