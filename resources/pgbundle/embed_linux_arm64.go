//go:build linux && arm64

package pgbundle

import "embed"

//go:embed bundles/linux-arm64/*
var bundleFS embed.FS

const bundleDir = "bundles/linux-arm64"
