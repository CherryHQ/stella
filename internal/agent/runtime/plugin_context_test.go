package runtime

import (
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPluginContextReturnsIndependentView(t *testing.T) {
	view := pkgplugins.SessionPluginView{
		RegisteredPluginIDs: []string{"plugin/a"},
		ExposedPluginIDs:    []string{"plugin/a"},
		SessionEnvSpecs:     []pkgplugins.SessionEnvSpec{{OAuthScopes: []string{"read"}}},
		PromptSections:      []pkgplugins.SystemPromptSection{{Content: "frozen"}},
		BinarySpecs: []pkgplugins.PluginBinarySpec{{Options: map[string]any{
			"token":  "secret",
			"nested": map[string]any{"scope": "private"},
			"items":  []any{map[string]any{"value": "original"}},
		}}},
	}
	ctx := PluginContext{view: view}
	view = ctx.SessionPluginView()
	view.SessionEnvSpecs[0].OAuthScopes[0] = "write"
	view.PromptSections[0].Content = "changed"
	view.RegisteredPluginIDs[0] = "plugin/changed"
	view.BinarySpecs[0].Options["token"] = "changed"
	view.BinarySpecs[0].Options["nested"].(map[string]any)["scope"] = "changed"
	view.BinarySpecs[0].Options["items"].([]any)[0].(map[string]any)["value"] = "changed"

	got := ctx.SessionPluginView()
	got.SessionEnvSpecs[0].OAuthScopes[0] = "admin"
	got.PromptSections[0].Content = "mutated"
	got.RegisteredPluginIDs[0] = "plugin/mutated"
	got.BinarySpecs[0].Options["token"] = "mutated"
	got.BinarySpecs[0].Options["nested"].(map[string]any)["scope"] = "mutated"
	got.BinarySpecs[0].Options["items"].([]any)[0].(map[string]any)["value"] = "mutated"
	want := ctx.SessionPluginView()
	nested := want.BinarySpecs[0].Options["nested"].(map[string]any)
	items := want.BinarySpecs[0].Options["items"].([]any)
	item := items[0].(map[string]any)
	if want.SessionEnvSpecs[0].OAuthScopes[0] != "read" || want.PromptSections[0].Content != "frozen" || want.RegisteredPluginIDs[0] != "plugin/a" || want.BinarySpecs[0].Options["token"] != "secret" || nested["scope"] != "private" || item["value"] != "original" {
		t.Fatalf("plugin context view was mutable: %#v", want)
	}
}
