//go:build darwin && arm64

package pgbundle

import "embed"

//go:embed bundles/darwin-arm64/*
var bundleFS embed.FS

const bundleDir = "bundles/darwin-arm64"
