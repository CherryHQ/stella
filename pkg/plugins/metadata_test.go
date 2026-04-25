package plugins

import (
	"reflect"
	"testing"
)

func TestPluginInfoCloneCopiesCapabilities(t *testing.T) {
	info := PluginInfo{
		ID:           "test",
		Capabilities: []string{"channel", "hook"},
	}
	clone := info.Clone()
	clone.Capabilities[0] = "changed"
	if info.Capabilities[0] != "channel" {
		t.Fatalf("original capabilities mutated after clone")
	}
}

func TestRegisteredPluginCloneIsIndependent(t *testing.T) {
	p := RegisteredPlugin{
		Info:         PluginInfo{ID: "x", Capabilities: []string{"tool"}},
		Capabilities: []string{"tool", "hook"},
		State:        PluginState{Config: map[string]any{"k": "v"}},
	}
	clone := p.Clone()
	clone.Capabilities[0] = "changed"
	clone.State.Config["k"] = "mutated"

	if p.Capabilities[0] != "tool" {
		t.Fatalf("original capabilities mutated")
	}
	if p.State.Config["k"] != "v" {
		t.Fatalf("original state config mutated")
	}
}

func TestRegisteredPluginSortedCapabilities(t *testing.T) {
	p := RegisteredPlugin{Capabilities: []string{"tool", "channel", "hook"}}
	got := p.SortedCapabilities()
	want := []string{"channel", "hook", "tool"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedCapabilities() = %v, want %v", got, want)
	}
	// must not mutate original
	if p.Capabilities[0] != "tool" {
		t.Fatalf("original capabilities mutated by SortedCapabilities")
	}
}

func TestStateScopeNormalize(t *testing.T) {
	tests := []struct {
		in   StateScope
		want StateScope
	}{
		{StateScope{}, StateScope{Kind: StateScopeGlobal}},
		{StateScope{Kind: StateScopeGlobal}, StateScope{Kind: StateScopeGlobal}},
		{StateScope{Kind: StateScopeUser, ID: "42"}, StateScope{Kind: StateScopeUser, ID: "42"}},
	}
	for _, tc := range tests {
		got := tc.in.Normalize()
		if got != tc.want {
			t.Errorf("Normalize(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
