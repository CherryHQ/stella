//go:build windows && amd64

package pgbundle

import "embed"

//go:embed bundles/windows-amd64/*
var bundleFS embed.FS

const bundleDir = "bundles/windows-amd64"
