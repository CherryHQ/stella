package web

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

// The OpenAPI definition and field enum connect Go's ownership source to the
// generated TypeScript types that the editor checks exhaustively. Keep that
// chain intact so a new Go definition field cannot be editable but unownable.
func TestManifestDefinitionFieldsStayInSyncWithOpenAPI(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "api", "spec", "domain", "plugins", "schemas.yaml"))
	if err != nil {
		t.Fatalf("read plugin schemas: %v", err)
	}
	var spec struct {
		Components struct {
			Schemas struct {
				Definition struct {
					Properties map[string]any `yaml:"properties"`
				} `yaml:"ManifestPluginDefinition"`
				Field struct {
					Enum []string `yaml:"enum"`
				} `yaml:"ManifestPluginDefinitionField"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(source, &spec); err != nil {
		t.Fatalf("parse plugin schemas: %v", err)
	}

	properties := make([]string, 0, len(spec.Components.Schemas.Definition.Properties))
	for name := range spec.Components.Schemas.Definition.Properties {
		properties = append(properties, name)
	}
	fields := spec.Components.Schemas.Field.Enum
	ownable := manifestplugins.OwnableFields()
	slices.Sort(properties)
	slices.Sort(fields)
	slices.Sort(ownable)
	if !slices.Equal(properties, ownable) {
		t.Errorf("OpenAPI definition properties = %v, Go ownable fields = %v", properties, ownable)
	}
	if !slices.Equal(fields, ownable) {
		t.Errorf("OpenAPI definition field enum = %v, Go ownable fields = %v", fields, ownable)
	}
}
