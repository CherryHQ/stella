package auth

import "testing"

func TestSyntheticFeishuEmailPreservesOAuthNormalization(t *testing.T) {
	cases := []struct {
		subject, tenant, want string
	}{
		{"on_union", "tenant-1", "on_union@tenant-1.feishu.local"},
		{" On Union ", "Tenant Key", "on-union@tenant-key.feishu.local"},
		{"!!!", "", "feishu-user@feishu.local"},
		{"member", "!!!", "member@tenant.feishu.local"},
	}
	for _, tt := range cases {
		if got := SyntheticFeishuEmail(tt.subject, tt.tenant); got != tt.want {
			t.Errorf("SyntheticFeishuEmail(%q, %q) = %q, want %q", tt.subject, tt.tenant, got, tt.want)
		}
	}
}
