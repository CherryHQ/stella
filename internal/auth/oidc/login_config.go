package oidc

// LoginConfig is the dynamic login-provider configuration, parsed once at the
// startup boundary (mirroring config.ServerConfig): the AUTH_OAUTH_*
// multi-provider block and the LOCAL_PASSWORD_/LOCAL_OIDC_ local-auth block.
// Setup consumes it instead of reading the process environment, so the oidc
// package has no os.Getenv of its own. It may hold secrets (OAuth client
// secrets) and must never be logged.
type LoginConfig struct {
	// OAuth is the validated AUTH_OAUTH_* provider block. Empty when no OAuth
	// login providers are configured.
	OAuth []*OAuthConfig
	// Local carries the raw LOCAL_PASSWORD_/LOCAL_OIDC_ values for local
	// password auth; the local package interprets them.
	Local LocalConfig
}

// LocalConfig holds the raw local-password auth values, preferring the
// LOCAL_PASSWORD_ names and falling back to the legacy LOCAL_OIDC_ names.
type LocalConfig struct {
	AllowRegistration   string
	AllowedEmailDomains string
}

// LoadLoginConfig parses the dynamic login-provider configuration through
// lookup (os.LookupEnv at the startup boundary). baseURL supplies the default
// OAuth redirect URL. It returns an error if any configured OAuth provider is
// invalid, so a misconfiguration fails fast at the boundary.
func LoadLoginConfig(lookup Lookup, baseURL string) (LoginConfig, error) {
	oauth, err := OAuthConfigs(lookup, baseURL)
	if err != nil {
		return LoginConfig{}, err
	}
	return LoginConfig{
		OAuth: oauth,
		Local: LocalConfig{
			AllowRegistration:   localValue(lookup, "ALLOW_REGISTRATION"),
			AllowedEmailDomains: localValue(lookup, "ALLOWED_EMAIL_DOMAINS"),
		},
	}, nil
}

// localValue reads a local-auth setting, preferring the LOCAL_PASSWORD_ prefix
// and falling back to the legacy LOCAL_OIDC_ prefix.
func localValue(lookup Lookup, name string) string {
	if v := envValue(lookup, "LOCAL_PASSWORD_"+name); v != "" {
		return v
	}
	return envValue(lookup, "LOCAL_OIDC_"+name)
}
