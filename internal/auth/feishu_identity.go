package auth

import "github.com/CherryHQ/stella/pkg/identity"

// SyntheticFeishuEmail returns the stable internal email used for Feishu members
// whose profile omits an email address. It is intentionally derived from the
// canonical union ID and tenant key so channel enrollment and OAuth converge.
func SyntheticFeishuEmail(subject, tenantKey string) string {
	return identity.SyntheticEmail(subject, tenantKey, "feishu.local")
}
