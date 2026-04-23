package manifestplugins

type ManifestPlugin struct {
	ID            string               `yaml:"id"`
	Kind          string               `yaml:"kind"`
	Name          string               `yaml:"name"`
	DisplayName   string               `yaml:"display_name"`
	Description   string               `yaml:"description"`
	Enabled       bool                 `yaml:"enabled"`
	Binaries      []ManifestBinary     `yaml:"binaries,omitempty"`
	Skills        []ManifestSkill      `yaml:"skills,omitempty"`
	SessionEnvs   []ManifestSessionEnv `yaml:"session_env,omitempty"`
	OAuthProvider string               `yaml:"oauth_provider,omitempty"`
}

type ManifestBinary struct {
	Name    string `yaml:"name"`
	Repo    string `yaml:"repo"`
	Version string `yaml:"version,omitempty"`
	BinPath string `yaml:"bin_path,omitempty"` // subdir within archive containing the binary
	Exe     string `yaml:"exe,omitempty"`      // binary name inside archive when it differs from Name
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

type ManifestOAuthFlow struct {
	Type          string `yaml:"type"`
	AuthURL       string `yaml:"auth_url,omitempty"`
	DeviceAuthURL string `yaml:"device_auth_url,omitempty"`
	TokenURL      string `yaml:"token_url"`
	AuthStyle     string `yaml:"auth_style,omitempty"`
}

type ManifestOAuthProvider struct {
	ID       string              `yaml:"id"`
	Scopes   []string            `yaml:"scopes"`
	VaultKey string              `yaml:"vault_key"`
	Flows    []ManifestOAuthFlow `yaml:"flows"`
}

type Manifest struct {
	OAuthProviders []ManifestOAuthProvider `yaml:"oauth_providers,omitempty"`
	Plugins        []ManifestPlugin        `yaml:"plugins"`
}

type rawManifestPlugin struct {
	ID            string               `yaml:"id"`
	Kind          string               `yaml:"kind"`
	Name          string               `yaml:"name"`
	DisplayName   string               `yaml:"display_name"`
	Description   string               `yaml:"description"`
	Enabled       *bool                `yaml:"enabled"`
	Binaries      []ManifestBinary     `yaml:"binaries,omitempty"`
	Skills        []ManifestSkill      `yaml:"skills,omitempty"`
	SessionEnvs   []ManifestSessionEnv `yaml:"session_env,omitempty"`
	OAuthProvider string               `yaml:"oauth_provider,omitempty"`
}

type rawManifestOAuthFlow struct {
	Type          string `yaml:"type"`
	AuthURL       string `yaml:"auth_url,omitempty"`
	DeviceAuthURL string `yaml:"device_auth_url,omitempty"`
	TokenURL      string `yaml:"token_url"`
	AuthStyle     string `yaml:"auth_style,omitempty"`
}

type rawManifestOAuthProvider struct {
	ID       string                 `yaml:"id"`
	Scopes   []string               `yaml:"scopes"`
	VaultKey string                 `yaml:"vault_key"`
	Flows    []rawManifestOAuthFlow `yaml:"flows"`
}

type rawManifest struct {
	OAuthProviders []rawManifestOAuthProvider `yaml:"oauth_providers,omitempty"`
	Plugins        []rawManifestPlugin        `yaml:"plugins"`
}
