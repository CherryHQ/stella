package connections

import "time"

// ProviderStatus describes the availability of an OAuth provider.
type ProviderStatus struct {
	Provider    string `json:"provider"`
	Icon        string `json:"icon,omitempty"`
	Available   bool   `json:"available"`
	Configured  bool   `json:"configured"` // true when DB or YAML has a client_id
	Connected   bool   `json:"connected"`
	Username    string `json:"username,omitempty"`    // label for the connected account
	Unavailable string `json:"unavailable,omitempty"` // reason when Available is false
	// RequiredBy lists display names of enabled tools that depend on this
	// provider being connected. Empty when no enabled tool needs it. Populated
	// at the server layer, which has access to the plugin manifest.
	RequiredBy []string `json:"required_by,omitempty"`
}

// OAuthProviderConfig holds the mutable credentials for an OAuth provider.
// ClientSecret is masked as "***" in read responses.
type OAuthProviderConfig struct {
	ProviderID   string `json:"provider_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url,omitempty"`
}

// FlowStatus is the model-visible view of an in-flight OAuth flow.
type FlowStatus struct {
	Provider        string
	FlowID          string
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	State           string
}

// VaultEntry holds non-sensitive metadata for a stored secret.
type VaultEntry struct {
	Name      string
	UpdatedAt string
}

// AddSecretInstruction is returned by add_secret; it never contains the secret value.
type AddSecretInstruction struct {
	Name    string
	Purpose string
	Command string // exact /config KEY VALUE command to run
}

// RunnerInvalidator closes live runners so the next session reloads vault/OAuth
// snapshots. Scope determines reach: a single user, one agent across all users,
// or every runner.
type RunnerInvalidator interface {
	InvalidateUser(userID string) error
	InvalidateAgent(agentID string) error
	InvalidateAll() error
}
