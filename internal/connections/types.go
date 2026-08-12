package connections

import "time"

// Reconnect reasons reported in ProviderStatus.ReconnectReason (D4). This is a
// Go-enforced closed enum; callers must not invent new values.
const (
	// ReconnectReasonCredentialsRotated means the connected bundle carries a
	// client_id that no longer matches the effective provider config.
	ReconnectReasonCredentialsRotated = "credentials_rotated"
	// ReconnectReasonMissingScopes means the granted scopes lack one or more
	// currently-requested scopes.
	ReconnectReasonMissingScopes = "missing_scopes"
)

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
	// RequestedScopes is the effective scope list the connect flow requests.
	RequestedScopes []string `json:"requested_scopes,omitempty"`
	// GrantedScopes is the scope set the stored token actually holds. Nil means
	// unknown (pre-capture bundle), which is distinct from "no scopes".
	GrantedScopes []string `json:"granted_scopes,omitempty"`
	// AccessExpiresAt / RefreshExpiresAt expose token health; zero when absent.
	AccessExpiresAt  time.Time `json:"access_expires_at,omitzero"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	// NeedsReconnect and ReconnectReason are computed at read time (D4).
	NeedsReconnect  bool   `json:"needs_reconnect,omitempty"`
	ReconnectReason string `json:"reconnect_reason,omitempty"`
}

// OAuthProviderConfig holds the mutable credentials for an OAuth provider.
// ClientSecret is masked as "***" in read responses.
type OAuthProviderConfig struct {
	ProviderID   string `json:"provider_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url,omitempty"`
	// Scopes is the administrator-configured minimum that every authorization
	// request includes. Empty means "no override, use the YAML default" (D2). It
	// is a floor, not a ceiling: a user may request additional scopes on top of
	// it, and the provider's own app configuration and consent screen bound what
	// can actually be granted.
	Scopes []string `json:"scopes,omitempty"`
	// DefaultScopes is the YAML seed default, output-only, so the UI can diff
	// and reset without a second call (D7). Ignored on writes.
	DefaultScopes []string `json:"default_scopes,omitempty"`
}

// FlowStatus is the model-visible view of an in-flight OAuth flow.
type FlowStatus struct {
	Provider        string
	FlowID          string
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	State           string
	Error           string // failure reason when State is "failed" (D5)
	RequestedScopes []string
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
