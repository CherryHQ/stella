// Package identity contains shared identity-format helpers used by adapters
// that must converge on the same durable representation.
package identity

import "strings"

// SyntheticEmail returns a stable synthetic email from a subject, tenant, and
// domain suffix. The suffix is supplied by the platform compatibility entry;
// this helper only performs the shared safe-label normalization.
func SyntheticEmail(subject, tenant, domainSuffix string) string {
	domainSuffix = strings.Trim(strings.ToLower(strings.TrimSpace(domainSuffix)), ".")
	if domainSuffix == "" {
		domainSuffix = "local"
	}
	fallbackPrefix := strings.TrimSuffix(domainSuffix, ".local")
	if fallbackPrefix == "" {
		fallbackPrefix = "user"
	}
	subject = normalizeLocalPart(subject, fallbackPrefix+"-user")
	if tenant == "" {
		return subject + "@" + domainSuffix
	}
	tenant = normalizeDomainLabel(tenant, "tenant")
	return subject + "@" + tenant + "." + domainSuffix
}

func normalizeLocalPart(s, fallback string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return fallback
	}
	return out
}

func normalizeDomainLabel(s, fallback string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallback
	}
	return out
}
