package plugin

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestApplyCLIWriteOnlyPatchPreservesTypedParameters(t *testing.T) {
	definition := Definition{Spec: publishedSpec(t, `{"binaries":[{"name":"uv","tool":"uv","version":"1","options":{"channel":"stable"}},{"name":"rg","tool":"github:BurntSushi/ripgrep","version":"13"}]}`)}
	current := json.RawMessage(`{"binaries":{"uv":{"version":"2","options":{"channel":"stable"}},"rg":{"version":"14"}},"mcp_servers":{"main":{"url":"https://example.com"}}}`)

	patched, err := applyCLIWriteOnlyPatch(definition, current, ConfigPatch{
		BinaryVersions: map[string]string{"uv": "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got ConfigParameters
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	if got.Binaries["uv"].Version != "3" || got.Binaries["uv"].Options["channel"] != "stable" {
		t.Fatalf("updated binary parameters = %#v", got.Binaries["uv"])
	}
	if got.Binaries["rg"].Version != "14" {
		t.Fatalf("existing binary pin was dropped: %#v", got.Binaries)
	}
	if got.MCPServers["main"].URL != "https://example.com" {
		t.Fatalf("other resource parameters were dropped: %#v", got.MCPServers)
	}
	var raw map[string]map[string]map[string]any
	if err := json.Unmarshal(patched, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["binaries"]["uv"]["tool"]; exists {
		t.Fatalf("patch leaked definition fields: %s", patched)
	}
}

func TestApplyCLIWriteOnlyPatchTreatsOnlyEmptyCurrentAsEmptyParameters(t *testing.T) {
	definition := Definition{Spec: publishedSpec(t, `{"binaries":[{"name":"uv","tool":"uv","version":"1"}]}`)}
	patched, err := applyCLIWriteOnlyPatch(definition, nil, ConfigPatch{
		BinaryVersions: map[string]string{"uv": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"binaries":{"uv":{"version":"2"}}}` {
		t.Fatalf("empty current patch = %s", patched)
	}

	_, err = applyCLIWriteOnlyPatch(definition, json.RawMessage(`{"binaries":[{"name":"uv","version":"2"}]}`), ConfigPatch{
		BinaryVersions: map[string]string{"uv": "3"},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("legacy array current error = %v, want ErrInvalidConfig", err)
	}
}

func TestApplyCLIWriteOnlyPatchRejectsUndeclaredAliases(t *testing.T) {
	definition := Definition{Spec: publishedSpec(t, `{"binaries":[{"name":"uv","tool":"uv","version":"1"}]}`)}
	_, err := applyCLIWriteOnlyPatch(definition, nil, ConfigPatch{
		BinaryVersions: map[string]string{"other": "2"},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown binary error = %v, want ErrInvalidConfig", err)
	}
	_, err = applyCLIWriteOnlyPatch(definition, json.RawMessage(`{"binaries":{"other":{"version":"2"}}}`), ConfigPatch{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown current binary error = %v, want ErrInvalidConfig", err)
	}
}
