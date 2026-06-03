package local

import "strings"

// Config holds local password authentication configuration.
type Config struct {
	// AllowRegistration controls whether local password self-registration stays
	// open after the first bootstrap user exists.
	AllowRegistration bool

	// BootstrapRegistration allows the first local user to create the admin
	// account even when ongoing self-registration is disabled.
	BootstrapRegistration bool

	// AllowedEmailDomains restricts self-registration to email addresses whose
	// domain matches one of these entries (case-insensitive, exact domain or
	// subdomain). An empty list allows any email domain.
	AllowedEmailDomains []string
}

// IsEmailAllowed reports whether email may self-register given the configured
// domain allowlist. An empty allowlist permits any domain. Matching is
// case-insensitive against the domain part, accepting the exact domain or any
// subdomain of an allowed entry.
func (c *Config) IsEmailAllowed(email string) bool {
	if len(c.AllowedEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range c.AllowedEmailDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// AllowRegistrationFromEnv parses LOCAL_PASSWORD_ALLOW_REGISTRATION. Empty means
// ongoing self-registration is closed; first-user bootstrap is handled separately.
func AllowRegistrationFromEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return false
	}
}

// SplitTrimmed splits a comma-separated string, trimming whitespace and
// dropping empty entries.
func SplitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
