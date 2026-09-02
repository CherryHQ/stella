package host

import (
	"sort"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// DefaultCatalogBundledSkillSpecs loads the default plugin catalog into a
// throw-away host and returns all registered bundled skill specs, sorted by
// skill name then plugin ID.
func DefaultCatalogBundledSkillSpecs() ([]pkgplugins.BundledSkillSpec, error) {
	// Channel plugins declare CapabilityChannelPlatform, which validation
	// requires be backed. Bind an (unconfigured) services bag so this
	// enumeration-only host can seal its catalog; it never runs a runtime.
	h := New(nil, WithChannelRuntimeServices(NewChannelRuntimeServices()))
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
