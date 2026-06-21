//go:build darwin && amd64

package pgbundle

import "embed"

//go:embed bundles/darwin-amd64/*
var bundleFS embed.FS

const bundleDir = "bundles/darwin-amd64"
