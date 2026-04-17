package pluginhost

import (
	"github.com/vaayne/anna/internal/tools"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// DefaultCatalogBinarySpecs loads the default plugin catalog into a throw-away
// host and returns all registered binary specs, deduplicated and sorted by name.
// Used by CLI commands that run outside the main server setup (tools list/install/upgrade, plugin enable).
func DefaultCatalogBinarySpecs() ([]pkgplugins.BinarySpec, error) {
	h := New(nil)
	if err := h.LoadDefaultCatalog(); err != nil {
		return nil, err
	}
	return tools.DeduplicateByName(h.AllBinarySpecs(), nil), nil
}
