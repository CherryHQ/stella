package manifestplugins

import "time"

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
	Repo        string    `json:"repo"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

type SkillInstallState struct {
	Repo        string    `json:"repo"`
	Name        string    `json:"name"`
	InstalledAt time.Time `json:"installed_at"`
}
