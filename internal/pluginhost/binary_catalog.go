package pluginhost

import pkgplugins "github.com/vaayne/anna/pkg/plugins"

// DefaultCatalogBinarySpecs loads the default plugin catalog into a throw-away
// host and returns all registered binary specs. Used by CLI commands that run
// outside the main server setup (tools list/install/upgrade, plugin enable).
func DefaultCatalogBinarySpecs() []pkgplugins.BinarySpec {
	h := New(nil)
	_ = h.LoadDefaultCatalog()
	return h.AllBinarySpecs()
}
