package connections

import "strings"

// splitGrantedScope parses the raw scope string a provider returned with a
// token. RFC 6749 says space-separated, but GitHub returns a comma-separated
// list ("repo,gist,read:org"); treating that as one scope made every GitHub
// connection report missing scopes and demand a reconnect forever.
func splitGrantedScope(raw string) []string {
	return normalizeScopes(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}))
}

// normalizeScopes trims whitespace, drops empty entries, and de-duplicates while
// preserving first-seen order. It is the write-boundary normalizer for
// admin-supplied scope overrides (D2): scope syntax is provider-specific, so we
// reject only empties, never on format.
func normalizeScopes(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// missingScopes returns requested scopes not present in granted, preserving
// requested order.
func missingScopes(requested, granted []string) []string {
	have := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		have[g] = struct{}{}
	}
	var out []string
	for _, r := range requested {
		if _, ok := have[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}

func unionScopes(groups ...[]string) []string {
	var combined []string
	for _, group := range groups {
		combined = append(combined, group...)
	}
	return normalizeScopes(combined)
}

// reconnectDecision computes whether a connected user must re-authorize and why
// (D4). grantedKnown is false when the stored bundle predates granted-scope
// capture, in which case scope drift cannot be asserted (unknown, not missing).
// Credential rotation takes precedence over missing scopes.
func reconnectDecision(bundleClientID, effectiveClientID string, requested, granted []string, grantedKnown bool) (bool, string) {
	if effectiveClientID != "" && bundleClientID != effectiveClientID {
		return true, ReconnectReasonCredentialsRotated
	}
	if grantedKnown && len(missingScopes(requested, granted)) > 0 {
		return true, ReconnectReasonMissingScopes
	}
	return false, ""
}
