package oidc

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const oauthProviderEnv = "AUTH_OAUTH_PROVIDERS"

// Lookup mirrors os.LookupEnv: it returns a variable's value and whether it was
// set. The login-config loaders take it so the oidc package never reads the
// process environment itself — the value flows in from the startup boundary
// (serverAction passes os.LookupEnv), exactly like config.LoadServerConfig.
type Lookup = func(string) (string, bool)

// envValue returns the value of key under lookup, or "" — os.Getenv semantics
// without touching the process environment.
func envValue(lookup Lookup, key string) string {
	v, _ := lookup(key)
	return v
}

var oauthProviderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type OAuthConfig struct {
	ProviderName string
	Kind         string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string

	AuthURL           string
	TokenURL          string
	TokenRequestStyle string
	UserInfoURL       string
	UserEmailsURL     string

	AllowedEmailDomains  []string
	AllowedTenantKeys    []string
	RequireEmailVerified bool
}

// OAuthConfigs parses the AUTH_OAUTH_* multi-provider block through lookup,
// validating each provider. baseURL supplies the default redirect URL when a
// provider does not set one explicitly.
func OAuthConfigs(lookup Lookup, baseURL string) ([]*OAuthConfig, error) {
	ids := splitTrimmed(envValue(lookup, oauthProviderEnv))
	out := make([]*OAuthConfig, 0, len(ids))
	var errs []string
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if _, ok := seen[id]; ok {
			errs = append(errs, fmt.Sprintf("duplicate OAuth provider %q", id))
			continue
		}
		seen[id] = struct{}{}
		cfg, err := OAuthConfigForProvider(lookup, id, baseURL)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out = append(out, cfg)
	}
	if len(errs) > 0 {
		return nil, errors.New("oauth login config: " + strings.Join(errs, "; "))
	}
	return out, nil
}

// OAuthConfigForProvider parses the AUTH_OAUTH_<PROVIDER>_* block for one
// provider through lookup.
func OAuthConfigForProvider(lookup Lookup, providerName, baseURL string) (*OAuthConfig, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if !oauthProviderIDPattern.MatchString(providerName) {
		return nil, fmt.Errorf("provider %q: id must match %s", providerName, oauthProviderIDPattern.String())
	}

	cfg := defaultOAuthConfig(providerName)
	prefix := "AUTH_OAUTH_" + envProviderName(providerName) + "_"
	cfg.ClientID = envValue(lookup, prefix+"CLIENT_ID")
	cfg.ClientSecret = envValue(lookup, prefix+"CLIENT_SECRET")
	cfg.RedirectURL = envValue(lookup, prefix+"REDIRECT_URL")
	cfg.AllowedEmailDomains = splitTrimmed(envValue(lookup, prefix+"ALLOWED_EMAIL_DOMAINS"))
	cfg.AllowedTenantKeys = splitTrimmed(envValue(lookup, prefix+"ALLOWED_TENANT_KEYS"))
	cfg.RequireEmailVerified = parseOAuthBool(envValue(lookup, prefix+"REQUIRE_EMAIL_VERIFIED"), cfg.RequireEmailVerified)

	if raw := envValue(lookup, prefix+"SCOPES"); raw != "" {
		cfg.Scopes = splitTrimmed(raw)
	}
	if v := envValue(lookup, prefix+"AUTH_URL"); v != "" {
		cfg.AuthURL = v
	}
	if v := envValue(lookup, prefix+"TOKEN_URL"); v != "" {
		cfg.TokenURL = v
	}
	if v := envValue(lookup, prefix+"TOKEN_REQUEST_STYLE"); v != "" {
		cfg.TokenRequestStyle = v
	}
	if v := envValue(lookup, prefix+"USERINFO_URL"); v != "" {
		cfg.UserInfoURL = v
	}
	if v := envValue(lookup, prefix+"USER_EMAILS_URL"); v != "" {
		cfg.UserEmailsURL = v
	}
	if cfg.RedirectURL == "" && baseURL != "" {
		cfg.RedirectURL = strings.TrimRight(baseURL, "/") + "/auth/callback/" + providerName
	}
	return cfg, cfg.Validate()
}

func defaultOAuthConfig(providerName string) *OAuthConfig {
	cfg := &OAuthConfig{
		ProviderName:         providerName,
		Kind:                 "generic",
		TokenRequestStyle:    "form",
		RequireEmailVerified: true,
	}
	switch providerName {
	case "github":
		cfg.Kind = "github"
		cfg.AuthURL = "https://github.com/login/oauth/authorize"
		cfg.TokenURL = "https://github.com/login/oauth/access_token"
		cfg.UserInfoURL = "https://api.github.com/user"
		cfg.UserEmailsURL = "https://api.github.com/user/emails"
		cfg.Scopes = []string{"read:user", "user:email"}
	case "google":
		cfg.Kind = "google"
		cfg.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
		cfg.TokenURL = "https://oauth2.googleapis.com/token"
		cfg.UserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
		cfg.Scopes = []string{"openid", "email", "profile"}
	case "feishu":
		cfg.Kind = "feishu"
		cfg.AuthURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
		cfg.TokenURL = "https://open.feishu.cn/open-apis/authen/v2/oauth/token"
		cfg.TokenRequestStyle = "json"
		cfg.UserInfoURL = "https://open.feishu.cn/open-apis/authen/v1/user_info"
		cfg.Scopes = []string{"contact:user.email:readonly"}
	}
	return cfg
}

func (c *OAuthConfig) Validate() error {
	var errs []string
	if c.ProviderName == "" {
		errs = append(errs, "provider name is required")
	} else if strings.EqualFold(c.ProviderName, "local") {
		errs = append(errs, "oauth provider name local is reserved")
	}
	if c.ClientID == "" {
		errs = append(errs, c.envName("CLIENT_ID")+" is required")
	}
	if c.ClientSecret == "" {
		errs = append(errs, c.envName("CLIENT_SECRET")+" is required")
	}
	if c.RedirectURL == "" {
		errs = append(errs, c.envName("REDIRECT_URL")+" or STELLA_BASE_URL is required")
	}
	if c.AuthURL == "" {
		errs = append(errs, c.envName("AUTH_URL")+" is required")
	}
	if c.TokenURL == "" {
		errs = append(errs, c.envName("TOKEN_URL")+" is required")
	}
	if c.UserInfoURL == "" {
		errs = append(errs, c.envName("USERINFO_URL")+" is required")
	}
	if c.TokenRequestStyle != "form" && c.TokenRequestStyle != "json" {
		errs = append(errs, c.envName("TOKEN_REQUEST_STYLE")+" must be form or json")
	}
	switch {
	case c.Kind == "feishu" && len(c.AllowedTenantKeys) == 0:
		errs = append(errs, c.envName("ALLOWED_TENANT_KEYS")+" is required for Feishu login")
	case c.Kind == "google" && len(c.AllowedTenantKeys) > 0:
		errs = append(errs, c.envName("ALLOWED_TENANT_KEYS")+" is not supported for Google login; use "+c.envName("ALLOWED_EMAIL_DOMAINS"))
	case len(c.AllowedEmailDomains) == 0 && len(c.AllowedTenantKeys) == 0:
		errs = append(errs, c.envName("ALLOWED_EMAIL_DOMAINS")+" or "+c.envName("ALLOWED_TENANT_KEYS")+" is required")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *OAuthConfig) envName(name string) string {
	return "AUTH_OAUTH_" + envProviderName(c.ProviderName) + "_" + name
}

func parseOAuthBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envProviderName(providerName string) string {
	var b strings.Builder
	for _, r := range providerName {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
