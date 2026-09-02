package host

import pkgplugins "github.com/CherryHQ/stella/pkg/plugins"

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
