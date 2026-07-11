package config

import (
	"encoding/json"
	"testing"
)

func TestSandboxConfigDefaults(t *testing.T) {
	c := SandboxConfig{}
	if got := c.NetworkMode(); got != SandboxNetworkAllowAll {
		t.Fatalf("NetworkMode() = %q, want %q", got, SandboxNetworkAllowAll)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}

func TestSandboxConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SandboxConfig
		wantErr bool
	}{
		{
			name: "empty config valid (defaults to disabled)",
			cfg:  SandboxConfig{},
		},
		{
			name: "allow_all valid",
			cfg:  SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkAllowAll}},
		},
		{
			name:    "invalid mode",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: "bogus"}},
			wantErr: true,
		},
		{
			name:    "allowlist without allow_all mode is invalid",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkDisabled, Allowlist: []string{"example.com"}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestSandboxConfigBackendIgnored(t *testing.T) {
	// Legacy "backend" key in JSON must be silently ignored.
	data := []byte(`{"backend":"docker","network":{"mode":"allow_all"}}`)
	var cfg SandboxConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if cfg.NetworkMode() != SandboxNetworkAllowAll {
		t.Fatalf("NetworkMode() = %q, want %q", cfg.NetworkMode(), SandboxNetworkAllowAll)
	}
}
