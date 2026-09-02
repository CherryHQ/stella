package config

import (
	"testing"
)

// TestSandboxNetworkConfig_Validate_DisabledMode verifies validation for disabled network mode.
func TestSandboxNetworkConfig_Validate_DisabledMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		allowlist []string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "disabled mode without allowlist is valid",
			mode:      SandboxNetworkDisabled,
			allowlist: nil,
			wantErr:   false,
		},
		{
			name:      "disabled mode with allowlist is invalid",
			mode:      SandboxNetworkDisabled,
			allowlist: []string{"example.com"},
			wantErr:   true,
			errMsg:    "allowlist requires whitelist mode",
		},
		{
			name:      "disabled mode with empty allowlist is valid",
			mode:      SandboxNetworkDisabled,
			allowlist: []string{},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SandboxConfig{
				Network: SandboxNetworkConfig{
					Mode:      tt.mode,
					Allowlist: tt.allowlist,
				},
			}
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, should contain %q", err, tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

// TestSandboxNetworkConfig_Validate_AllowAllMode verifies validation for allow_all network mode.
func TestSandboxNetworkConfig_Validate_AllowAllMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		allowlist []string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "allow_all mode without allowlist is valid",
			mode:      SandboxNetworkAllowAll,
			allowlist: nil,
			wantErr:   false,
		},
		{
			name:      "allow_all mode with allowlist is invalid",
			mode:      SandboxNetworkAllowAll,
			allowlist: []string{"example.com"},
			wantErr:   true,
			errMsg:    "allowlist requires whitelist mode",
		},
		{
			name:      "allow_all mode with empty allowlist is valid",
			mode:      SandboxNetworkAllowAll,
			allowlist: []string{},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SandboxConfig{
				Network: SandboxNetworkConfig{
					Mode:      tt.mode,
					Allowlist: tt.allowlist,
				},
			}
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, should contain %q", err, tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

// TestSandboxNetworkConfig_Validate_InvalidMode verifies validation for invalid network modes.
func TestSandboxNetworkConfig_Validate_InvalidMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{
			name: "unknown mode is invalid",
			mode: "unknown",
		},
		{
			name: "empty mode defaults to disabled and is valid",
			mode: "",
		},
		{
			name: "typo in mode is invalid",
			mode: "disable",
		},
		{
			name: "mixed case mode is invalid",
			mode: "Disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SandboxConfig{
				Network: SandboxNetworkConfig{
					Mode:      tt.mode,
					Allowlist: nil,
				},
			}
			err := cfg.Validate()
			if tt.mode == "" {
				// Empty mode defaults to disabled, which is valid
				if err != nil {
					t.Errorf("Validate() with empty mode should default to disabled and be valid, got error: %v", err)
				}
			} else if err == nil {
				// Other invalid modes should produce errors
				t.Errorf("Validate() expected error for mode %q, got nil", tt.mode)
			}
		})
	}
}

// TestSandboxNetworkConfig_NetworkMode verifies the NetworkMode method.
func TestSandboxNetworkConfig_NetworkMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
	}{
		{
			name:     "empty mode returns allow_all",
			mode:     "",
			expected: SandboxNetworkAllowAll,
		},
		{
			name:     "disabled mode is returned",
			mode:     SandboxNetworkDisabled,
			expected: SandboxNetworkDisabled,
		},
		{
			name:     "allow_all mode is returned",
			mode:     SandboxNetworkAllowAll,
			expected: SandboxNetworkAllowAll,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SandboxConfig{
				Network: SandboxNetworkConfig{Mode: tt.mode},
			}
			got := cfg.NetworkMode()
			if got != tt.expected {
				t.Errorf("NetworkMode() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// containsString is a helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
