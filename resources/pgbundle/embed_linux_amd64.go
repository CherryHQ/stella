//go:build linux && amd64

package pgbundle

import "embed"

//go:embed bundles/linux-amd64/*
var bundleFS embed.FS

const bundleDir = "bundles/linux-amd64"
