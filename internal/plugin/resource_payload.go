package plugin

// ResourcePayload is the persisted resource declaration shared by release
// definitions and scoped configurations. Identity and ownership stay on their
// enclosing Definition and Config.
type ResourcePayload struct {
	Description string               `json:"description,omitempty"`
	Category    string               `json:"category,omitempty"`
	Prompt      string               `json:"prompt,omitempty"`
	Binaries    []BinaryResource     `json:"binaries,omitempty"`
	Skills      []SkillResource      `json:"skills,omitempty"`
	SessionEnvs []SessionEnvResource `json:"session_env,omitempty"`
	// OAuthProvider preserves the shorthand stored by existing configurations.
	OAuthProvider string                       `json:"oauth_provider,omitempty"`
	OAuth         []OAuthRequirement           `json:"oauth,omitempty"`
	MCPServers    map[string]MCPServerResource `json:"mcp_servers,omitempty"`
}

type BinaryResource struct {
	// Name is the executable alias in an authorized session selection.
	Name string `json:"name"`

	// Tool is the mise tool key, e.g.:
	//   uv   bun   github:cli/cli   pipx:mypy   npm:serve   http:sentinel
	Tool string `json:"tool"`

	// Version to install; defaults to "latest" when omitted.
	Version string `json:"version,omitempty"`

	// Options are mise tool options, using the same names as mise.toml.
	Options map[string]any `json:"options,omitempty"`
}

type SkillResource struct {
	// Name is the local, release-owned skill identity. A manifest cannot point
	// at a repository or another source; the asset descriptor owns its bytes.
	Name string `json:"name"`
}

type SessionEnvResource struct {
	EnvVar   string `json:"env_var"`
	Source   string `json:"source"`
	Value    string `json:"value,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type OAuthRequirement struct {
	Provider string         `json:"provider"`
	Scopes   []string       `json:"scopes,omitempty"`
	Bindings []OAuthBinding `json:"bindings,omitempty"`
}

type OAuthBinding struct {
	Credential string `json:"credential"`
	EnvVar     string `json:"env_var,omitempty"`
	Connection string `json:"connection,omitempty"`
}

type MCPServerResource struct {
	URL            string            `json:"url"`
	Transport      string            `json:"transport"`
	AuthType       string            `json:"auth_type"`
	CredentialMode string            `json:"credential_mode,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}
