package feishu

import (
	"testing"

	"github.com/vaayne/anna/internal/feishutool"
)

func TestAuthURL(t *testing.T) {
	url := feishutool.AuthURL("cli_test123", "https://example.com/callback", "ou_abc")
	if url == "" {
		t.Fatal("AuthURL returned empty string")
	}

	expected := "https://open.feishu.cn/open-apis/authen/v1/authorize?app_id=cli_test123&redirect_uri=https%3A%2F%2Fexample.com%2Fcallback&state=ou_abc"
	if url != expected {
		t.Errorf("AuthURL = %q, want %q", url, expected)
	}
}

func TestAuthURLEmptyRedirect(t *testing.T) {
	url := feishutool.AuthURL("cli_test123", "", "state123")
	if url == "" {
		t.Fatal("AuthURL returned empty string")
	}
	// Should still produce a valid URL structure (no redirect_uri when empty).
	expected := "https://open.feishu.cn/open-apis/authen/v1/authorize?app_id=cli_test123&state=state123"
	if url != expected {
		t.Errorf("AuthURL = %q, want %q", url, expected)
	}
}
