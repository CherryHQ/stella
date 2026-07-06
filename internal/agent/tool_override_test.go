package agent

import "testing"

func TestResolveToolOverridePrecedence(t *testing.T) {
	tests := []struct {
		name        string
		defaultOn   bool
		rows        []ToolOverride
		wantEnabled bool
		wantOrigin  string
	}{
		{name: "default on", defaultOn: true, wantEnabled: true, wantOrigin: ToolOverrideOriginDefault},
		{name: "default off", defaultOn: false, wantEnabled: false, wantOrigin: ToolOverrideOriginDefault},
		{name: "system enables", rows: []ToolOverride{{ToolName: "x", Scope: ToolOverrideScopeSystem, Enabled: true}}, wantEnabled: true, wantOrigin: ToolOverrideScopeSystem},
		{name: "system agent beats system", rows: []ToolOverride{{ToolName: "x", Scope: ToolOverrideScopeSystem, Enabled: false}, {ToolName: "x", Scope: ToolOverrideScopeSystemAgent, Enabled: true}}, wantEnabled: true, wantOrigin: ToolOverrideScopeSystemAgent},
		{name: "user disables after admin enable", rows: []ToolOverride{{ToolName: "x", Scope: ToolOverrideScopeSystem, Enabled: true}, {ToolName: "x", Scope: ToolOverrideScopeUser, Enabled: false}}, wantEnabled: false, wantOrigin: ToolOverrideScopeUser},
		{name: "user agent beats user", rows: []ToolOverride{{ToolName: "x", Scope: ToolOverrideScopeUser, Enabled: false}, {ToolName: "x", Scope: ToolOverrideScopeUserAgent, Enabled: true}}, wantEnabled: true, wantOrigin: ToolOverrideScopeUserAgent},
		{name: "admin disable beats user enable", rows: []ToolOverride{{ToolName: "x", Scope: ToolOverrideScopeSystemAgent, Enabled: false}, {ToolName: "x", Scope: ToolOverrideScopeUserAgent, Enabled: true}}, wantEnabled: false, wantOrigin: ToolOverrideScopeSystemAgent},
		{name: "other tool ignored", defaultOn: true, rows: []ToolOverride{{ToolName: "y", Scope: ToolOverrideScopeUserAgent, Enabled: false}}, wantEnabled: true, wantOrigin: ToolOverrideOriginDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveToolOverride(tt.defaultOn, "x", tt.rows)
			if got.Enabled != tt.wantEnabled || got.Origin != tt.wantOrigin {
				t.Fatalf("ResolveToolOverride() = (%v, %q), want (%v, %q)", got.Enabled, got.Origin, tt.wantEnabled, tt.wantOrigin)
			}
		})
	}
}

func TestResolveToolOverrideCoreExemption(t *testing.T) {
	rows := []ToolOverride{{ToolName: "bash", Scope: ToolOverrideScopeSystemAgent, Enabled: false}}
	got := ResolveToolOverride(false, "bash", rows)
	if !got.Enabled || got.Origin != ToolOverrideOriginDefault {
		t.Fatalf("core tool decision = (%v, %q), want enabled default", got.Enabled, got.Origin)
	}
}
