package manifestplugins

import (
	"path/filepath"
	"time"
)

// StatePath returns the path for the manifest state file.
func StatePath(annaHome string) string {
	return filepath.Join(annaHome, "plugin-manifest-state.json")
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
	Name        string    `json:"name"`
	Tool        string    `json:"tool"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

type SkillInstallState struct {
	Repo        string    `json:"repo"`
	Name        string    `json:"name"`
	InstalledAt time.Time `json:"installed_at"`
}
