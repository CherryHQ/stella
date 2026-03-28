package config

import "path/filepath"

func PluginsPath() string {
	return filepath.Join(AnnaHome(), "plugins")
}

func BundledPluginsPath() string {
	return filepath.Join(PluginsPath(), "bundled")
}

func InstalledPluginsPath() string {
	return filepath.Join(PluginsPath(), "installed")
}
