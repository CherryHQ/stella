package auth

import "strings"

// SyntheticFeishuEmail returns the stable internal email used for Feishu members
// whose profile omits an email address. It is intentionally derived from the
// canonical union ID and tenant key so channel enrollment and OAuth converge.
func SyntheticFeishuEmail(subject, tenantKey string) string {
	subject = feishuEmailLocalPart(subject)
	if tenantKey == "" {
		return subject + "@feishu.local"
	}
	return subject + "@" + feishuEmailDomainLabel(tenantKey) + ".feishu.local"
}

func feishuEmailLocalPart(s string) string {
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
		return "feishu-user"
	}
	return out
}

func feishuEmailDomainLabel(s string) string {
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
		return "tenant"
	}
	return out
}
