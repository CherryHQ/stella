package host

import (
	"sort"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// DefaultCatalogBundledSkillSpecs loads the default plugin catalog into a
// throw-away host and returns all registered bundled skill specs, sorted by
// skill name then plugin ID.
func DefaultCatalogBundledSkillSpecs() ([]pkgplugins.BundledSkillSpec, error) {
	// Catalog loading validates static registrations only. Capability backing is
	// checked by Seal/runtime startup, so this read-only enumeration has no
	// runtime service or account capability to fabricate.
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
