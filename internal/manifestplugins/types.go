package manifestplugins

type ManifestPlugin struct {
	ID                       string               `json:"id" yaml:"id"`
	Kind                     string               `json:"kind" yaml:"kind"`
	Name                     string               `json:"name" yaml:"name"`
	DisplayName              string               `json:"display_name" yaml:"display_name"`
	Description              string               `json:"description" yaml:"description"`
	Enabled                  bool                 `json:"enabled" yaml:"enabled"`
	Prompt                   string               `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Binaries                 []ManifestBinary     `json:"binaries,omitempty" yaml:"binaries,omitempty"`
	Skills                   []ManifestSkill      `json:"skills,omitempty" yaml:"skills,omitempty"`
	SessionEnvs              []ManifestSessionEnv `json:"session_env,omitempty" yaml:"session_env,omitempty"`
	OAuthProvider            string               `json:"oauth_provider,omitempty" yaml:"oauth_provider,omitempty"`
	OAuthProviderConfigField string               `json:"oauth_provider_config_field,omitempty" yaml:"oauth_provider_config_field,omitempty"`
	OAuthProviderChoices     []string             `json:"oauth_provider_choices,omitempty" yaml:"oauth_provider_choices,omitempty"`
}

type ManifestBinary struct {
	// Name is the binary name written to $STELLA_HOME/bin/.
	Name string `json:"name" yaml:"name"`

	// Tool is the mise tool key in "backend:identifier" format, e.g.:
	//   github:cli/cli   pipx:mypy   npm:serve   http:sentinel
	Tool string `json:"tool" yaml:"tool"`

	// URL is required for the http backend (the download URL template).
	URL string `json:"url,omitempty" yaml:"url,omitempty"`

	// Version to install; defaults to "latest" when omitted.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	// Shared asset options (github + http)
	StripComponents int    `json:"strip_components,omitempty" yaml:"strip_components,omitempty"`
	BinPath         string `json:"bin_path,omitempty" yaml:"bin_path,omitempty"`
	Bin             string `json:"bin,omitempty" yaml:"bin,omitempty"`
	RenameExe       string `json:"rename_exe,omitempty" yaml:"rename_exe,omitempty"`
	Checksum        string `json:"checksum,omitempty" yaml:"checksum,omitempty"`

	// GitHub-only options
	AssetPattern  string `json:"asset_pattern,omitempty" yaml:"asset_pattern,omitempty"`
	VersionPrefix string `json:"version_prefix,omitempty" yaml:"version_prefix,omitempty"`
	NoApp         bool   `json:"no_app,omitempty" yaml:"no_app,omitempty"`
	FilterBins    string `json:"filter_bins,omitempty" yaml:"filter_bins,omitempty"`
	Prerelease    bool   `json:"prerelease,omitempty" yaml:"prerelease,omitempty"`
	APIURL        string `json:"api_url,omitempty" yaml:"api_url,omitempty"`

	// HTTP-only options
	Size            string `json:"size,omitempty" yaml:"size,omitempty"`
	Format          string `json:"format,omitempty" yaml:"format,omitempty"`
	VersionListURL  string `json:"version_list_url,omitempty" yaml:"version_list_url,omitempty"`
	VersionRegex    string `json:"version_regex,omitempty" yaml:"version_regex,omitempty"`
	VersionJSONPath string `json:"version_json_path,omitempty" yaml:"version_json_path,omitempty"`
	VersionExpr     string `json:"version_expr,omitempty" yaml:"version_expr,omitempty"`

	// Pipx-only options
	Extras   string `json:"extras,omitempty" yaml:"extras,omitempty"`
	PipxArgs string `json:"pipx_args,omitempty" yaml:"pipx_args,omitempty"`
	UVX      bool   `json:"uvx,omitempty" yaml:"uvx,omitempty"`
	UVXArgs  string `json:"uvx_args,omitempty" yaml:"uvx_args,omitempty"`
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
	ID                       string               `yaml:"id"`
	Kind                     string               `yaml:"kind"`
	Name                     string               `yaml:"name"`
	DisplayName              string               `yaml:"display_name"`
	Description              string               `yaml:"description"`
	Enabled                  *bool                `yaml:"enabled"`
	Prompt                   string               `yaml:"prompt,omitempty"`
	Binaries                 []ManifestBinary     `yaml:"binaries,omitempty"`
	Skills                   []ManifestSkill      `yaml:"skills,omitempty"`
	SessionEnvs              []ManifestSessionEnv `yaml:"session_env,omitempty"`
	OAuthProvider            string               `yaml:"oauth_provider,omitempty"`
	OAuthProviderConfigField string               `yaml:"oauth_provider_config_field,omitempty"`
	OAuthProviderChoices     []string             `yaml:"oauth_provider_choices,omitempty"`
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
