package plugin

// ResourcePayload is the declaration stored in Definition.Spec and the
// resolved execution payload. Persisted Config uses ConfigParameters instead.
type ResourcePayload struct {
	Version       string            `json:"version,omitempty"`
	ContentDigest string            `json:"content_digest,omitempty"`
	Content       *ContentReference `json:"content,omitempty"`
	// Origin distinguishes immutable directory packages from personal remote
	// MCP definitions. It is authored in the Definition.Spec and never a
	// configuration parameter.
	Origin      string                       `json:"origin,omitempty"`
	Description string                       `json:"description,omitempty"`
	Category    string                       `json:"category,omitempty"`
	Prompt      string                       `json:"prompt,omitempty"`
	Binaries    []BinaryResource             `json:"binaries,omitempty"`
	Skills      []SkillResource              `json:"skills,omitempty"`
	SessionEnvs []SessionEnvResource         `json:"session_env,omitempty"`
	OAuth       []OAuthRequirement           `json:"oauth,omitempty"`
	MCPServers  map[string]MCPServerResource `json:"mcp_servers,omitempty"`
}

// ContentReference binds a published definition to the complete immutable
// asset tree. The tree digest is separate from content_digest, which covers
// the canonical definition envelope containing this reference.
type ContentReference struct {
	Digest string `json:"digest"`
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
	// Path and Description are package metadata. Builtin declarations may omit
	// Path; the runtime derives the conventional skills/<name>/SKILL.md path.
	Path        string `json:"path,omitempty"`
	Description string `json:"description,omitempty"`
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
	Description    string            `json:"description,omitempty"`
	URL            string            `json:"url"`
	Transport      string            `json:"transport"`
	AuthType       string            `json:"auth_type"`
	CredentialMode string            `json:"credential_mode,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}
