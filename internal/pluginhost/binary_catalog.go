package pluginhost

import (
	"sort"

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

// DefaultCatalogBundledSkillSpecs loads the default plugin catalog into a
// throw-away host and returns all registered bundled skill specs, sorted by
// skill name then plugin ID.
func DefaultCatalogBundledSkillSpecs() ([]pkgplugins.BundledSkillSpec, error) {
	h := New(nil)
	if err := h.LoadDefaultCatalog(); err != nil {
		return nil, err
	}
	specs := h.AllBundledSkillSpecs()
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Name != specs[j].Name {
			return specs[i].Name < specs[j].Name
		}
		return specs[i].PluginID < specs[j].PluginID
	})
	return specs, nil
}
