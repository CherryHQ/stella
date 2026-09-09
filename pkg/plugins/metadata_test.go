package plugins

import (
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
