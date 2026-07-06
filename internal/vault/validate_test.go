package vault_test

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/vault"
)

func TestValidateName(t *testing.T) {
	t.Parallel()

	valid := []string{
		"GITHUB_TOKEN",
		"MY_SECRET",
		"A",
		"API_KEY_2",
		"Z9",
		"MY_VAR_123",
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			t.Parallel()
			if err := vault.ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalidFormat := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"lowercase", "lowercase"},
		{"starts_with_digit", "123START"},
		{"has_space", "HAS SPACE"},
		{"too_long", strings.Repeat("A", 129)},
		{"starts_lower", "aUPPER"},
		{"hyphen", "MY-VAR"},
	}
	for _, tc := range invalidFormat {
		t.Run("invalid_format/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := vault.ValidateName(tc.input); err == nil {
				t.Errorf("ValidateName(%q) = nil, want error", tc.input)
			}
		})
	}

	reserved := []struct {
		name  string
		input string
	}{
		{"STELLA_HOME", "STELLA_HOME"},
		{"STELLA_TOKEN", "STELLA_TOKEN"},
		{"PATH", "PATH"},
		{"HOME", "HOME"},
		{"LC_ALL", "LC_ALL"},
		{"XDG_CONFIG_HOME", "XDG_CONFIG_HOME"},
		{"TMPDIR", "TMPDIR"},
		{"USER", "USER"},
		{"SHELL", "SHELL"},
		{"LANG", "LANG"},
		{"TERM", "TERM"},
		{"PATH_EXTRA", "PATH_EXTRA"},
	}
	for _, tc := range reserved {
		t.Run("reserved/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := vault.ValidateName(tc.input); err == nil {
				t.Errorf("ValidateName(%q) = nil, want reserved error", tc.input)
			}
		})
	}
}
