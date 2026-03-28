package config

import "testing"

func TestLoadRuntimePluginBindingsDefaults(t *testing.T) {
	store := setupDBStore(t)

	bindings, err := LoadRuntimePluginBindings(store)
	if err != nil {
		t.Fatalf("LoadRuntimePluginBindings: %v", err)
	}

	if got := bindings.ToolBinding("read"); got != "tool/read" {
		t.Fatalf("ToolBinding(read) = %q, want tool/read", got)
	}
	if got := bindings.ChannelBinding("telegram"); got != "channel/telegram" {
		t.Fatalf("ChannelBinding(telegram) = %q, want channel/telegram", got)
	}
}

func TestSaveRuntimePluginBindingsRoundTrip(t *testing.T) {
	store := setupDBStore(t)

	bindings := DefaultRuntimePluginBindings()
	bindings.Tools["read"] = "tool/custom-read"
	bindings.Channels["telegram"] = "channel/custom-telegram"

	if err := SaveRuntimePluginBindings(store, bindings); err != nil {
		t.Fatalf("SaveRuntimePluginBindings: %v", err)
	}

	got, err := LoadRuntimePluginBindings(store)
	if err != nil {
		t.Fatalf("LoadRuntimePluginBindings: %v", err)
	}

	if id := got.ToolBinding("read"); id != "tool/custom-read" {
		t.Fatalf("ToolBinding(read) = %q, want tool/custom-read", id)
	}
	if id := got.ChannelBinding("telegram"); id != "channel/custom-telegram" {
		t.Fatalf("ChannelBinding(telegram) = %q, want channel/custom-telegram", id)
	}
	if id := got.ToolBinding("bash"); id != "tool/bash" {
		t.Fatalf("ToolBinding(bash) = %q, want default tool/bash", id)
	}
}
