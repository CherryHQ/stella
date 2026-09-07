package plugin

import "testing"

func TestResourceDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload ResourcePayload
		invalid bool
	}{
		{name: "metadata only"},
		{name: "missing tool", payload: ResourcePayload{Binaries: []BinaryResource{{Name: "x"}}}, invalid: true},
		{name: "mise registry tool", payload: ResourcePayload{Binaries: []BinaryResource{{Name: "uv", Tool: "uv"}}}},
		{name: "mise owns tool key syntax", payload: ResourcePayload{Binaries: []BinaryResource{{Name: "x", Tool: "github:repo"}}}},
		{name: "invalid env source", payload: ResourcePayload{SessionEnvs: []SessionEnvResource{{EnvVar: "MY_TOKEN", Source: "invalid_source"}}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResourceDeclarations(tc.payload, "test release", nil)
			if (err != nil) != tc.invalid {
				t.Fatalf("validation = %v, want invalid=%v", err, tc.invalid)
			}
		})
	}
}

func TestResourceDeclarationsCheckProviderReferences(t *testing.T) {
	for _, payload := range []ResourcePayload{
		{OAuthProvider: "missing"},
		{OAuth: []OAuthRequirement{{Provider: "missing"}}},
	} {
		if err := ValidateResourceDeclarations(payload, "release", nil); err != nil {
			t.Fatalf("resource-only validation: %v", err)
		}
		if err := ValidateResourceDeclarations(payload, "release", map[string]struct{}{"known": {}}); err == nil {
			t.Fatal("release with unknown provider was accepted")
		}
	}
}

func TestResourceDefinitionRequiresIdentity(t *testing.T) {
	definition := Definition{DisplayName: "Demo", Source: SourceBuiltin, Revision: 1, Spec: []byte(`{"binaries":[{"name":"x","tool":"github:a/b"}]}`)}
	if err := definition.Validate(); err == nil {
		t.Fatal("definition without an ID was accepted")
	}
}
