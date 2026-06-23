//go:build !stella_embed_pg && darwin && amd64

package pgbundle

import "embed"

//go:embed bundles/darwin-amd64/.keep
var bundleFS embed.FS

const bundleDir = "bundles/darwin-amd64"
