package skills

import (
	"context"
	"testing"
)

func TestGitHubSource(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{"owner/repo@skill", true},
		{"owner/repo", true},
		{"https://github.com/owner/repo", true},
		{"https://github.com/owner/repo/tree/main/path", true},
		{"https://gitlab.com/owner/repo", false},
		{"clawhub:my-skill", false},
		{"clawhub:my-skill@1.0.0", false},
		{"/local/path", false},
		{"./relative", false},
	}
	for _, c := range cases {
		if got := GitHubSource(c.source); got != c.want {
			t.Errorf("GitHubSource(%q) = %v, want %v", c.source, got, c.want)
		}
	}
}

func TestInjectGitHubToken(t *testing.T) {
	got := injectGitHubToken("https://github.com/owner/repo.git", "tok123")
	want := "https://x-access-token:tok123@github.com/owner/repo.git"
	if got != want {
		t.Errorf("injectGitHubToken = %q, want %q", got, want)
	}

	// Non-github URLs are left untouched.
	gl := "https://gitlab.com/owner/repo.git"
	if got := injectGitHubToken(gl, "tok"); got != gl {
		t.Errorf("injectGitHubToken(gitlab) = %q, want unchanged", got)
	}
}

func TestGitHubTokenContext(t *testing.T) {
	ctx := context.Background()
	if tok := githubTokenFromContext(ctx); tok != "" {
		t.Errorf("empty ctx token = %q, want empty", tok)
	}
	// Empty token is a no-op.
	if ctx2 := WithGitHubToken(ctx, ""); githubTokenFromContext(ctx2) != "" {
		t.Error("WithGitHubToken(ctx, \"\") should not store a token")
	}
	ctx = WithGitHubToken(ctx, "abc")
	if tok := githubTokenFromContext(ctx); tok != "abc" {
		t.Errorf("ctx token = %q, want abc", tok)
	}
}
