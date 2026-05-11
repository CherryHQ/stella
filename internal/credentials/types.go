package credentials

import "time"

// ProviderStatus describes the availability of an OAuth provider.
type ProviderStatus struct {
	Provider    string `json:"provider"`
	Available   bool   `json:"available"`
	Configured  bool   `json:"configured"` // true when DB or YAML has a client_id
	Connected   bool   `json:"connected"`
	Username    string `json:"username,omitempty"`    // label for the connected account
	Unavailable string `json:"unavailable,omitempty"` // reason when Available is false
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

// RunnerInvalidator invalidates live runners for a user across all pools.
type RunnerInvalidator interface {
	InvalidateUser(userID int64) error
}
