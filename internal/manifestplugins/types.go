package manifestplugins

type ManifestPlugin struct {
	ID          string `json:"id" yaml:"id"`
	Kind        string `json:"kind" yaml:"kind"`
	Name        string `json:"name" yaml:"name"`
	DisplayName string `json:"display_name" yaml:"display_name"`
	Description string `json:"description" yaml:"description"`
	Enabled     bool   `json:"enabled" yaml:"enabled"`

	// Category is a display-only hint for grouping in the settings UI
	// ("system", "integration", "tool"). Empty means the UI derives a bucket
	// from the plugin's signals (oauth → integration, hook → system, else tool).
	Category string `json:"category,omitempty" yaml:"category,omitempty"`
	// Essential marks a plugin the runtime depends on (e.g. rg/fd back the
	// Grep/Glob tools); the UI guards against disabling it.
	Essential bool `json:"essential,omitempty" yaml:"essential,omitempty"`

	Prompt string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	// Capabilities lists the host Platform ports this plugin requires. Threaded
	// into PluginInfo.RequiredCapabilities so manifest plugins declare, and the
	// host validates, their capability needs like Go-registered plugins.
	Capabilities  []string             `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Binaries      []ManifestBinary     `json:"binaries,omitempty" yaml:"binaries,omitempty"`
	Skills        []ManifestSkill      `json:"skills,omitempty" yaml:"skills,omitempty"`
	SessionEnvs   []ManifestSessionEnv `json:"session_env,omitempty" yaml:"session_env,omitempty"`
	OAuthProvider string               `json:"oauth_provider,omitempty" yaml:"oauth_provider,omitempty"`
}

type ManifestBinary struct {
	// Name is the binary name written to $STELLA_HOME/bin/.
	Name string `json:"name" yaml:"name"`

	// Tool is the mise tool key, e.g.:
	//   uv   bun   github:cli/cli   pipx:mypy   npm:serve   http:sentinel
	Tool string `json:"tool" yaml:"tool"`

	// Version to install; defaults to "latest" when omitted.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	// Options are mise tool options, using the same names as mise.toml.
	Options map[string]any `json:"options,omitempty" yaml:",inline"`
}

type ManifestSkill struct {
	Repo string `json:"repo" yaml:"repo"`
	Name string `json:"name" yaml:"name"`
}

type ManifestSessionEnv struct {
	EnvVar   string `json:"env_var" yaml:"env_var"`
	Source   string `json:"source" yaml:"source"`
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

type ManifestOAuthFlow struct {
	Type          string `json:"type" yaml:"type"`
	AuthURL       string `json:"auth_url,omitempty" yaml:"auth_url,omitempty"`
	DeviceAuthURL string `json:"device_auth_url,omitempty" yaml:"device_auth_url,omitempty"`
	TokenURL      string `json:"token_url" yaml:"token_url"`
	AuthStyle     string `json:"auth_style,omitempty" yaml:"auth_style,omitempty"`
	PKCE          bool   `json:"pkce,omitempty" yaml:"pkce,omitempty"`
}

type ManifestOAuthProvider struct {
	ID           string              `json:"id" yaml:"id"`
	Icon         string              `json:"icon,omitempty" yaml:"icon,omitempty"`
	Scopes       []string            `json:"scopes" yaml:"scopes"`
	VaultKey     string              `json:"vault_key" yaml:"vault_key"`
	Flows        []ManifestOAuthFlow `json:"flows" yaml:"flows"`
	ClientID     string              `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	ClientSecret string              `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`
}

type Manifest struct {
	OAuthProviders []ManifestOAuthProvider `json:"oauth_providers,omitempty" yaml:"oauth_providers,omitempty"`
	Plugins        []ManifestPlugin        `json:"plugins" yaml:"plugins"`
}

type rawManifestPlugin struct {
	ID            string               `yaml:"id"`
	Kind          string               `yaml:"kind"`
	Name          string               `yaml:"name"`
	DisplayName   string               `yaml:"display_name"`
	Description   string               `yaml:"description"`
	Enabled       *bool                `yaml:"enabled"`
	Category      string               `yaml:"category,omitempty"`
	Essential     bool                 `yaml:"essential,omitempty"`
	Prompt        string               `yaml:"prompt,omitempty"`
	Capabilities  []string             `yaml:"capabilities,omitempty"`
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
	PKCE          bool   `yaml:"pkce,omitempty"`
}

type rawManifestOAuthProvider struct {
	ID           string                 `yaml:"id"`
	Icon         string                 `yaml:"icon,omitempty"`
	Scopes       []string               `yaml:"scopes"`
	VaultKey     string                 `yaml:"vault_key"`
	Flows        []rawManifestOAuthFlow `yaml:"flows"`
	ClientID     string                 `yaml:"client_id,omitempty"`
	ClientSecret string                 `yaml:"client_secret,omitempty"`
}

type rawManifest struct {
	OAuthProviders []rawManifestOAuthProvider `yaml:"oauth_providers,omitempty"`
	Plugins        []rawManifestPlugin        `yaml:"plugins"`
}
