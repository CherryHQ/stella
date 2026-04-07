package pluginhost

import pkgplugins "github.com/vaayne/anna/pkg/plugins"

func defaultCatalog() *pkgplugins.Catalog {
	catalog := pkgplugins.NewCatalog()
	for _, id := range pkgplugins.Names() {
		plugin, ok := pkgplugins.Get(id)
		if ok {
			catalog.Register(id, plugin)
		}
	}
	return catalog
}
