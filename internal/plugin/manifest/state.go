package manifest

import (
	"path/filepath"
	"time"
)

// StatePath returns the path for the manifest state file.
func StatePath(stellaHome string) string {
	return filepath.Join(stellaHome, "plugin-manifest-state.json")
}

type ManifestState struct {
	UpdatedAt time.Time                     `json:"updated_at"`
	Plugins   map[string]PluginInstallState `json:"plugins"`
}

type PluginInstallState struct {
	Binaries []BinaryInstallState `json:"binaries,omitempty"`
	Skills   []SkillInstallState  `json:"skills,omitempty"`
}

type BinaryInstallState struct {
	Name string `json:"name"`
	Tool string `json:"tool"`
	// Spec is the version spec requested by the manifest (e.g. "2.40", "latest").
	// Cache hits key on it; an empty Spec (pre-Spec state files) always misses
	// and re-resolves, repopulating it.
	Spec string `json:"spec,omitempty"`
	// Version is the concrete version mise resolved for Spec at install time.
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

type SkillInstallState struct {
	Repo        string    `json:"repo"`
	Name        string    `json:"name"`
	InstalledAt time.Time `json:"installed_at"`
}
