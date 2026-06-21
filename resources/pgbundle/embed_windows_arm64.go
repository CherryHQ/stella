//go:build windows && arm64

package pgbundle

import "embed"

//go:embed bundles/windows-arm64/*
var bundleFS embed.FS

const bundleDir = "bundles/windows-arm64"
