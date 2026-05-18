// Package resources bundles embedded resources (skills, souls, delegates, templates,
// and builtin plugin manifest) that Stella ships with its binary.
// Runtime code accesses them through Registry, not by walking the filesystem directly.
package resources

import (
	"embed"
	"io/fs"
)

//go:embed all:skills all:souls all:delegates all:templates
var fsys embed.FS

//go:embed plugins.yaml
var builtinPluginsYAML []byte

// BuiltinPluginsYAML returns the raw bytes of the builtin plugin manifest.
func BuiltinPluginsYAML() []byte { return builtinPluginsYAML }

// FS returns the full embedded filesystem rooted at the package directory.
// Prefer SubFS for kind-scoped access.
func FS() fs.FS { return fsys }

// SubFS returns the embedded filesystem scoped to a single Kind's subdirectory.
// Empty (nil, false) when the kind has no known subdir.
func SubFS(kind Kind) (fs.FS, bool) {
	sub := kind.subdir()
	if sub == "" {
		return nil, false
	}
	sf, err := fs.Sub(fsys, sub)
	if err != nil {
		return nil, false
	}
	return sf, true
}
