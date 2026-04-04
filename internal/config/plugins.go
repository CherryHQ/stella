package config

import "path/filepath"

func PluginsPath() string {
	return filepath.Join(AnnaHome(), "plugins")
}

func InstalledPluginsPath() string {
	return filepath.Join(PluginsPath(), "installed")
}
