package connections

import "strings"

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

func allowedScopes(scopes, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, scope := range allowed {
		allowedSet[scope] = struct{}{}
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if _, ok := allowedSet[scope]; ok {
			out = append(out, scope)
		}
	}
	return normalizeScopes(out)
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
