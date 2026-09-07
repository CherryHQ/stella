package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
	"github.com/CherryHQ/stella/resources/binaries"
)

func TestSystemRuntimeGenerationUsesOnlyImmutableReleaseCommands(t *testing.T) {
	catalog := &manifest.Manifest{Plugins: []manifest.ManifestPlugin{
		{ID: "fd", Enabled: true, ManifestPluginDefinition: manifest.ManifestPluginDefinition{Binaries: []manifest.ManifestBinary{{Name: "fd", Tool: "github:sharkdp/fd", Version: "10.4.2"}}}},
		{ID: "injected", Enabled: true, ManifestPluginDefinition: manifest.ManifestPluginDefinition{Binaries: []manifest.ManifestBinary{{Name: "injected", Tool: "npm:injected"}}}},
		{ID: "xberg", Enabled: false, ManifestPluginDefinition: manifest.ManifestPluginDefinition{Skills: []manifest.ManifestSkill{{Name: "xberg"}}}},
	}}
	before, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range binaries.KnownRuntimeNames() {
		if !strings.Contains(string(before), `Name: "`+name+`", Embedded: true`) {
			t.Fatalf("missing embedded release command %s", name)
		}
	}
	for _, name := range []string{"fd", "injected"} {
		if strings.Contains(string(before), `Name: "`+name+`"`) {
			t.Fatalf("authored command %s changed the embedded release map", name)
		}
	}
	if !strings.Contains(string(before), `"builtin:xberg"`) {
		t.Fatal("lost Xberg skill platform restriction")
	}
	for i := range catalog.Plugins {
		catalog.Plugins[i].Enabled = !catalog.Plugins[i].Enabled
	}
	after, err := renderSystemRuntimes(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("plugin enablement changed the embedded release map")
	}
}
