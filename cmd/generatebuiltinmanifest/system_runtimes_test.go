package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func TestSystemRuntimeGenerationIncludesDisabledAndNewCLIs(t *testing.T) {
	catalog := &manifest.Manifest{Plugins: []manifest.ManifestPlugin{
		{ID: "system/new-cli", Kind: "system", Enabled: false, ManifestPluginDefinition: manifest.ManifestPluginDefinition{
			Binaries: []manifest.ManifestBinary{{Name: "new-cli", Tool: "github:example/new-cli", Version: "1.2.3", Options: map[string]any{"exe": "real-cli"}}},
			Skills:   []manifest.ManifestSkill{{Name: "one"}, {Name: "two"}},
		}},
		{ID: "system/embedded", Kind: "system", Enabled: false, BundledBinaries: []string{"embedded"}},
		{ID: "tool/optional", Kind: "tool", Enabled: true, ManifestPluginDefinition: manifest.ManifestPluginDefinition{
			Binaries: []manifest.ManifestBinary{{Name: "optional", Tool: "optional"}},
		}},
	}}
	before, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Name: "new-cli"`, `MiseTool: "github:example/new-cli"`, `Version: "1.2.3"`, `"exe": "real-cli"`, `"builtin:one", "builtin:two"`, `Name: "embedded", Embedded: true`} {
		if !strings.Contains(string(before), want) {
			t.Errorf("runtime projection missing %s", want)
		}
	}
	if strings.Contains(string(before), `Name: "optional"`) {
		t.Fatal("optional CLI entered the required runtime projection")
	}
	for i := range catalog.Plugins {
		catalog.Plugins[i].Enabled = !catalog.Plugins[i].Enabled
	}
	after, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("plugin enablement changed the required runtime projection")
	}
}
