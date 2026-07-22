package webhook

import "testing"

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		raw     map[string]any
		wantErr bool
	}{
		{"provider required", map[string]any{}, true},
		{"ephemeral ok", map[string]any{"provider": "generic", "session_mode": "ephemeral"}, false},
		{"persistent ok", map[string]any{"provider": "generic", "session_mode": "persistent"}, false},
		{"github ok", map[string]any{"provider": "github", "github_events": []any{"push"}, "github_repositories": []any{"acme/repo"}}, false},
		{"github missing allowlists", map[string]any{"provider": "github"}, true},
		{"generic with github allowlist", map[string]any{"provider": "generic", "github_events": []any{"push"}}, true},
		{"unknown provider", map[string]any{"provider": "gitlab"}, true},
		{"bad session_mode", map[string]any{"session_mode": "weird"}, true},
		{"negative wait", map[string]any{"wait_timeout_seconds": -1}, true},
		{"negative max run", map[string]any{"max_run_timeout_seconds": -5}, true},
		{"wait over ceiling", map[string]any{"wait_timeout_seconds": 601}, true},
		{"max run over ceiling", map[string]any{"max_run_timeout_seconds": 3601}, true},
		{"valid full", map[string]any{
			"provider":                "generic",
			"default_wait":            true,
			"wait_timeout_seconds":    30,
			"max_run_timeout_seconds": 120,
			"session_mode":            "persistent",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
