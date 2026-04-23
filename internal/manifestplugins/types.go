package manifestplugins

type ManifestPlugin struct {
	ID          string               `yaml:"id"`
	Kind        string               `yaml:"kind"`
	Name        string               `yaml:"name"`
	DisplayName string               `yaml:"display_name"`
	Description string               `yaml:"description"`
	Enabled     bool                 `yaml:"enabled"`
	Binaries    []ManifestBinary     `yaml:"binaries,omitempty"`
	Skills      []ManifestSkill      `yaml:"skills,omitempty"`
	SessionEnvs []ManifestSessionEnv `yaml:"session_env,omitempty"`
}

type ManifestBinary struct {
	Name      string            `yaml:"name"`
	Repo      string            `yaml:"repo"`
	Version   string            `yaml:"version,omitempty"`
	Platforms map[string]string `yaml:"platforms,omitempty"` // mise platform key → asset_pattern glob
	BinPath   string            `yaml:"bin_path,omitempty"`
	Bin       string            `yaml:"bin,omitempty"`
}

type ManifestSkill struct {
	Repo string `yaml:"repo"`
	Name string `yaml:"name"`
}

type ManifestSessionEnv struct {
	EnvVar   string `yaml:"env_var"`
	Source   string `yaml:"source"`
	Value    string `yaml:"value,omitempty"`
	Required bool   `yaml:"required,omitempty"`
}

type Manifest struct {
	Plugins []ManifestPlugin `yaml:"plugins"`
}

type rawManifestPlugin struct {
	ID          string               `yaml:"id"`
	Kind        string               `yaml:"kind"`
	Name        string               `yaml:"name"`
	DisplayName string               `yaml:"display_name"`
	Description string               `yaml:"description"`
	Enabled     *bool                `yaml:"enabled"`
	Binaries    []ManifestBinary     `yaml:"binaries,omitempty"`
	Skills      []ManifestSkill      `yaml:"skills,omitempty"`
	SessionEnvs []ManifestSessionEnv `yaml:"session_env,omitempty"`
}

type rawManifest struct {
	Plugins []rawManifestPlugin `yaml:"plugins"`
}
