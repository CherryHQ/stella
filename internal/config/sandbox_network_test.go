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
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
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
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestSandboxNetworkConfig_Validate_WhitelistMode verifies validation for whitelist network mode.
func TestSandboxNetworkConfig_Validate_WhitelistMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		allowlist []string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "whitelist mode with valid hostname is valid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"example.com"},
			wantErr:   false,
		},
		{
			name:      "whitelist mode with valid IP is valid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"192.168.1.1"},
			wantErr:   false,
		},
		{
			name:      "whitelist mode with valid CIDR is valid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"192.168.0.0/24"},
			wantErr:   false,
		},
		{
			name:      "whitelist mode with multiple valid entries is valid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"example.com", "192.168.1.1", "10.0.0.0/8"},
			wantErr:   false,
		},
		{
			name:      "whitelist mode without allowlist is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: nil,
			wantErr:   true,
			errMsg:    "allowlist is required",
		},
		{
			name:      "whitelist mode with empty allowlist is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{},
			wantErr:   true,
			errMsg:    "allowlist is required",
		},
		{
			name:      "whitelist mode with empty entry is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"example.com", ""},
			wantErr:   true,
			errMsg:    "must not be empty",
		},
		{
			name:      "whitelist mode with invalid hostname is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"host_name"},
			wantErr:   true,
			errMsg:    "must be an IP, CIDR, or hostname",
		},
		{
			name:      "whitelist mode with path-like entry is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"example.com/path"},
			wantErr:   true,
			errMsg:    "must be an IP, CIDR, or hostname",
		},
		{
			name:      "whitelist mode with port in entry is invalid",
			mode:      SandboxNetworkWhitelist,
			allowlist: []string{"example.com:8080"},
			wantErr:   true,
			errMsg:    "must be an IP, CIDR, or hostname",
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
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
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
			} else {
				// Other invalid modes should produce errors
				if err == nil {
					t.Errorf("Validate() expected error for mode %q, got nil", tt.mode)
				}
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
			name:     "empty mode returns disabled",
			mode:     "",
			expected: SandboxNetworkDisabled,
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
		{
			name:     "whitelist mode is returned",
			mode:     SandboxNetworkWhitelist,
			expected: SandboxNetworkWhitelist,
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

// TestSandboxNetworkConfig_AllowlistEntryValidation verifies allowlist entry validation.
func TestSandboxNetworkConfig_AllowlistEntryValidation(t *testing.T) {
	tests := []struct {
		name    string
		entry   string
		wantErr bool
		errMsg  string
	}{
		// Valid entries
		{name: "valid hostname", entry: "example.com", wantErr: false},
		{name: "valid hostname with subdomain", entry: "sub.example.com", wantErr: false},
		{name: "valid hostname with hyphen", entry: "my-host.example.com", wantErr: false},
		{name: "valid IPv4", entry: "192.168.1.1", wantErr: false},
		{name: "valid IPv4 CIDR", entry: "192.168.0.0/24", wantErr: false},
		{name: "valid IPv6", entry: "::1", wantErr: false},
		{name: "valid IPv6 full", entry: "2001:db8::1", wantErr: false},
		{name: "valid IPv6 CIDR", entry: "2001:db8::/32", wantErr: false},

		// Invalid entries
		{name: "empty entry", entry: "", wantErr: true, errMsg: "must not be empty"},
		{name: "whitespace only", entry: "   ", wantErr: true, errMsg: "must not be empty"},
		{name: "hostname with path", entry: "example.com/path", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with port", entry: "example.com:8080", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with scheme", entry: "https://example.com", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with query", entry: "example.com?query", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with fragment", entry: "example.com#anchor", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with space", entry: "example .com", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with slash", entry: "example/com", wantErr: true, errMsg: "must be an IP"},
		{name: "hostname with backslash", entry: "example\\com", wantErr: true, errMsg: "must be an IP"},
		{name: "wildcard hostname", entry: "*.example.com", wantErr: true, errMsg: "must be an IP"},
		{name: "invalid CIDR", entry: "192.168.0.0/33", wantErr: true, errMsg: "must be an IP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SandboxConfig{
				Network: SandboxNetworkConfig{
					Mode:      SandboxNetworkWhitelist,
					Allowlist: []string{tt.entry},
				},
			}
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error for entry %q, got nil", tt.entry)
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, should contain %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error for entry %q: %v", tt.entry, err)
				}
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
