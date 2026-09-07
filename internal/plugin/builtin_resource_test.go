package plugin

import (
	"slices"
	"testing"
)

func TestBuiltinResourceCatalog(t *testing.T) {
	definitions, err := BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) == 0 {
		t.Fatal("release catalog is empty")
	}
	found := make(map[string]bool)
	for _, definition := range definitions {
		if slices.Contains([]string{"mise", "xberg", "fd", "rg"}, definition.ID) {
			found[definition.ID] = true
		}
		payload, err := DecodeResourcePayload(definition.Spec, definition.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateResourceDeclarations(payload, definition.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"mise", "xberg", "fd", "rg"} {
		if !found[id] {
			t.Fatalf("release resource %q is missing", id)
		}
	}
}

func TestBuiltinLarkCLIUsesManagedFeishuOAuth(t *testing.T) {
	definitions, err := BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.ID != "lark-cli" {
			continue
		}
		payload, err := DecodeResourcePayload(definition.Spec, definition.ID)
		if err != nil {
			t.Fatal(err)
		}
		if payload.OAuthProvider != "feishu" {
			t.Fatalf("OAuthProvider = %q, want feishu", payload.OAuthProvider)
		}
		if len(payload.SessionEnvs) != 3 {
			t.Fatalf("SessionEnvs = %#v, want token, app ID, and brand injection", payload.SessionEnvs)
		}
		if len(payload.Binaries) != 1 || payload.Binaries[0].Version != "1.0.87" {
			t.Fatalf("Binaries = %#v, want pinned lark-cli 1.0.87", payload.Binaries)
		}
		return
	}
	t.Fatal("lark-cli not found")
}
