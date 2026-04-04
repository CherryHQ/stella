package main

import (
	"testing"

	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

func TestResolveChannelPluginDefinitionLooksByName(t *testing.T) {
	catalog := pluginhost.NewCatalog()
	err := catalog.Add(pluginhost.Definition{
		Manifest: pluginapi.Manifest{
			Name:            "telegram",
			Version:         "1.0.0",
			Kind:            pluginapi.KindChannel,
			ProtocolVersion: pluginapi.ProtocolVersion,
			Entrypoint:      pluginhost.BuiltinEntrypoint,
		},
	})
	if err != nil {
		t.Fatalf("catalog.Add: %v", err)
	}

	def, err := resolveChannelPluginDefinition(catalog, "telegram", "", "")
	if err != nil {
		t.Fatalf("resolveChannelPluginDefinition: %v", err)
	}
	if got := def.ID(); got != "channel/telegram" {
		t.Fatalf("resolveChannelPluginDefinition() = %q", got)
	}
}

func TestPluginChannelKeepsSlotName(t *testing.T) {
	ch := newChannelPlugin("telegram", pluginhost.Definition{
		Manifest: pluginapi.Manifest{
			Name:            "replacement-telegram",
			Version:         "1.0.0",
			Kind:            pluginapi.KindChannel,
			ProtocolVersion: pluginapi.ProtocolVersion,
			Entrypoint:      pluginhost.BuiltinEntrypoint,
		},
	})

	if got := ch.Name(); got != "telegram" {
		t.Fatalf("Name() = %q, want slot name telegram", got)
	}
}
