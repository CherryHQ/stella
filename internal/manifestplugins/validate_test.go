package manifestplugins

import (
	"testing"
)

func TestValidate_NoID(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{Binaries: []ManifestBinary{{Name: "x", Tool: "github:a/b"}}},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for plugin with no ID")
	}
}

func TestValidate_NoCapabilities(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{ID: "tool/empty"},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for plugin with no binaries/skills/session_env")
	}
}

func TestValidate_BinaryNoTool(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID:       "tool/x",
			Binaries: []ManifestBinary{{Name: "x"}},
		},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for binary with no tool")
	}
}

func TestValidate_BinaryGithubNoSlash(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID:       "tool/x",
			Binaries: []ManifestBinary{{Name: "x", Tool: "github:repo"}},
		},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for github tool without owner/repo format")
	}
}

func TestValidate_SessionEnvInvalidSource(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID: "tool/x",
			SessionEnvs: []ManifestSessionEnv{
				{EnvVar: "MY_TOKEN", Source: "invalid_source"},
			},
		},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for session_env with invalid source")
	}
}

func TestValidate_ValidManifest(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(m); err != nil {
		t.Errorf("expected no error for builtin manifest, got: %v", err)
	}
}
