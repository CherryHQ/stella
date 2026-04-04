package main

import (
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

func TestResolveChannelPluginDefinitionUsesRuntimeBinding(t *testing.T) {
	catalog := pluginhost.NewCatalog()
	err := catalog.Add(pluginhost.Definition{
		Manifest: pluginapi.Manifest{
			Name:            "replacement-telegram",
			Version:         "1.0.0",
			Kind:            pluginapi.KindChannel,
			ProtocolVersion: pluginapi.ProtocolVersion,
			Entrypoint:      pluginhost.BuiltinEntrypoint,
		},
	})
	if err != nil {
		t.Fatalf("catalog.Add: %v", err)
	}

	bindings := config.DefaultRuntimePluginBindings()
	bindings.Channels["telegram"] = "channel/replacement-telegram"

	def, err := resolveChannelPluginDefinition(catalog, bindings, "telegram", "", "")
	if err != nil {
		t.Fatalf("resolveChannelPluginDefinition: %v", err)
	}
	if got := def.ID(); got != "channel/replacement-telegram" {
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
