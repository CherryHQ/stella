//go:build !stella_embed_pg && linux && amd64

package pgbundle

import "embed"

//go:embed bundles/linux-amd64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/linux-amd64"
