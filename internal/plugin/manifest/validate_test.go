package manifest

import (
	"testing"
)

func TestValidate_NoID(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{ManifestPluginDefinition: ManifestPluginDefinition{Binaries: []ManifestBinary{{Name: "x", Tool: "github:a/b"}}}},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for plugin with no ID")
	}
}

func TestValidate_RequiresManifestContent(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{ID: "empty"},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for plugin with no binaries/skills/session_env")
	}
}

func TestValidate_BinaryNoTool(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID:                       "x",
			ManifestPluginDefinition: ManifestPluginDefinition{Binaries: []ManifestBinary{{Name: "x"}}},
		},
	}}
	if err := Validate(m); err == nil {
		t.Error("expected error for binary with no tool")
	}
}

func TestValidate_BinaryMiseRegistryTool(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID:                       "uv",
			ManifestPluginDefinition: ManifestPluginDefinition{Binaries: []ManifestBinary{{Name: "uv", Tool: "uv"}}},
		},
	}}
	if err := Validate(m); err != nil {
		t.Errorf("expected no error for mise registry tool, got: %v", err)
	}
}

func TestValidate_LeavesToolKeysToMise(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID:                       "x",
			ManifestPluginDefinition: ManifestPluginDefinition{Binaries: []ManifestBinary{{Name: "x", Tool: "github:repo"}}},
		},
	}}
	if err := Validate(m); err != nil {
		t.Errorf("expected no error for mise-owned tool key validation, got: %v", err)
	}
}

func TestValidate_SessionEnvInvalidSource(t *testing.T) {
	m := &Manifest{Plugins: []ManifestPlugin{
		{
			ID: "x",
			ManifestPluginDefinition: ManifestPluginDefinition{
				SessionEnvs: []ManifestSessionEnv{
					{EnvVar: "MY_TOKEN", Source: "invalid_source"},
				},
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
