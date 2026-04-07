package mcp

import "testing"

func TestDecodeConfig(t *testing.T) {
	raw := map[string]any{
		"servers": []any{
			map[string]any{
				"name":            " GitHub ",
				"enabled":         true,
				"transport":       TransportStdio,
				"command":         "npx",
				"args":            []any{"-y", "server"},
				"env":             map[string]any{"TOKEN": "secret"},
				"timeout_seconds": float64(45),
			},
		},
	}

	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("len(cfg.Servers) = %d, want 1", len(cfg.Servers))
	}
	server := cfg.Servers[0]
	if server.Name != "GitHub" {
		t.Fatalf("server.Name = %q, want GitHub", server.Name)
	}
	if server.Transport != TransportStdio {
		t.Fatalf("server.Transport = %q", server.Transport)
	}
	if server.Command != "npx" {
		t.Fatalf("server.Command = %q", server.Command)
	}
	if server.TimeoutSeconds != 45 {
		t.Fatalf("server.TimeoutSeconds = %d, want 45", server.TimeoutSeconds)
	}
	if got := server.Env["TOKEN"]; got != "secret" {
		t.Fatalf("server.Env[TOKEN] = %q, want secret", got)
	}
}

func TestDecodeConfigRejectsDuplicateNames(t *testing.T) {
	_, err := DecodeConfig(map[string]any{
		"servers": []any{
			map[string]any{"name": "dup", "transport": TransportStdio, "command": "a"},
			map[string]any{"name": "Dup", "transport": TransportStdio, "command": "b"},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate server name error")
	}
}

func TestServerConfigValidateByTransport(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{name: "stdio valid", cfg: ServerConfig{Name: "a", Transport: TransportStdio, Command: "cmd"}},
		{name: "stdio missing command", cfg: ServerConfig{Name: "a", Transport: TransportStdio}, wantErr: true},
		{name: "sse valid", cfg: ServerConfig{Name: "a", Transport: TransportSSE, URL: "https://example.com/sse"}},
		{name: "http missing url", cfg: ServerConfig{Name: "a", Transport: TransportHTTP}, wantErr: true},
	}

	for _, tc := range cases {
		err := tc.cfg.Validate()
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
	}
}
