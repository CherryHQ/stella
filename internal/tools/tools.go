package tools

import "path/filepath"

// BinDir returns the tools binary directory path within stellaHome.
func BinDir(stellaHome string) string {
	return filepath.Join(stellaHome, "bin")
}
