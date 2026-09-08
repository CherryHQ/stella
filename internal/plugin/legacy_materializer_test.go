package plugin

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestMaterializePackageBuiltinGeneratesManifestAndSkill(t *testing.T) {
	definition := Definition{
		ID:          "builtin-demo",
		DisplayName: "Builtin Demo",
		Source:      SourceBuiltin,
		Spec:        json.RawMessage(`{"version":"1.2.3","skills":[{"name":"hello"}]}`),
	}
	result, err := MaterializePackage(t.Context(), definition, nil, nil, func(context.Context, string, string) (map[string][]byte, map[string]fs.FileMode, error) {
		return map[string][]byte{"SKILL.md": []byte("---\nname: hello\ndescription: hello\n---\n")}, map[string]fs.FileMode{"SKILL.md": 0o755}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(func(yield func(string) bool) {
		for _, file := range result.Files {
			yield(file.Path)
		}
	}); !slices.Equal(got, []string{"plugin.json", "skills/hello/SKILL.md"}) {
		t.Fatalf("files=%v", got)
	}
	if result.Files[1].Mode.Perm() != 0o755 {
		t.Fatalf("skill mode=%#o, want executable bits preserved", result.Files[1].Mode.Perm())
	}
	pkg, diagnostics := loadMaterializedPackage(t, result.Files)
	if pkg.Manifest.Name != definition.ID || pkg.Manifest.Version != "1.2.3" || len(pkg.Skills) != 1 || diagnostics.HasErrors() {
		t.Fatalf("package=%+v diagnostics=%+v", pkg, diagnostics)
	}
}

func TestMaterializePackageRejectsUnsafeCapturedPath(t *testing.T) {
	definition := Definition{ID: "custom-demo", DisplayName: "Custom Demo", Source: SourceCustom, Spec: json.RawMessage(`{"skills":[]}`)}
	_, err := MaterializePackage(t.Context(), definition, nil, map[string]ResourceFile{
		"../escape": {Data: []byte("x"), Mode: 0o644},
	}, nil)
	if err == nil {
		t.Fatal("unsafe source path was accepted")
	}
}

func TestMaterializePackagePreservesMCPCredentialRefsInManifest(t *testing.T) {
	definition := Definition{
		ID:          "credentialed-package",
		DisplayName: "Credentialed Package",
		Source:      SourceBuiltin,
		Spec: json.RawMessage(`{"mcp_servers":{
			"bearer":{"url":"https://bearer.example","transport":"sse","auth_type":"bearer","credential_mode":"shared"},
			"oauth":{"url":"https://oauth.example","transport":"streamable_http","auth_type":"oauth","credential_mode":"shared","metadata":{"oauth":{"client_id":"client"}}}
		}}`),
	}
	config := Config{
		ID: "credentialed-config", PluginID: definition.ID, Scope: ScopeSystem,
		Payload: definition.Spec,
		CredentialRefs: json.RawMessage(`{"mcp_servers":{
			"bearer":{"bearer":{"name":"BEARER_REF"}},
			"oauth":{"oauth_bundle":{"name":"BUNDLE_REF"},"oauth_client_secret":{"name":"CLIENT_REF"}}
		}}`),
	}
	result, err := MaterializePackage(t.Context(), definition, &config, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := loadMaterializedPackage(t, result.Files)
	if diagnostics.HasErrors() || pkg == nil || pkg.Extension == nil {
		t.Fatalf("package=%+v diagnostics=%+v", pkg, diagnostics)
	}
	bearer := pkg.Extension.MCPAuth["bearer"]
	if bearer.CredentialRef != "BEARER_REF" {
		t.Fatalf("bearer credential ref=%q, want BEARER_REF", bearer.CredentialRef)
	}
	oauth := pkg.Extension.MCPAuth["oauth"]
	if oauth.CredentialRef != "BUNDLE_REF" || oauth.ClientSecretRef != "CLIENT_REF" {
		t.Fatalf("oauth credential refs=%+v", oauth)
	}
}

func TestMaterializePackagePreservesRefsWhenPayloadIsInherited(t *testing.T) {
	definition := Definition{
		ID:     "inherited-refs",
		Source: SourceBuiltin,
		Spec:   json.RawMessage(`{"mcp_servers":{"main":{"url":"https://mcp.example","transport":"sse","auth_type":"bearer","credential_mode":"shared"}}}`),
	}
	config := Config{
		ID: "inherited-config", PluginID: definition.ID, Scope: ScopeSystem,
		CredentialRefs: json.RawMessage(`{"mcp_servers":{"main":{"bearer":{"name":"INHERITED_REF"}}}}`),
	}
	result, err := materializePackage(t.Context(), definition, nil, config.CredentialRefs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := loadMaterializedPackage(t, result.Files)
	if diagnostics.HasErrors() || pkg == nil || pkg.Extension == nil {
		t.Fatalf("package=%+v diagnostics=%+v", pkg, diagnostics)
	}
	if got := pkg.Extension.MCPAuth["main"].CredentialRef; got != "INHERITED_REF" {
		t.Fatalf("credential ref=%q, want INHERITED_REF", got)
	}
}

func TestMaterializeRemoteMCPKeepsCredentialEvidenceAndScopedNames(t *testing.T) {
	spec := json.RawMessage(`{"origin":"remote_mcp","mcp_servers":{"main":{"url":"https://a.example","transport":"sse","auth_type":"bearer"},"other":{"url":"https://b.example","transport":"streamable_http","auth_type":"oauth","metadata":{"oauth":{"client_id":"public"}}}}}`)
	configPayload := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://a.example","transport":"sse","auth_type":"bearer"},"other":{"url":"https://b.example","transport":"streamable_http","auth_type":"oauth","metadata":{"oauth":{"client_id":"public"}}}}}`)
	result, err := MaterializeRemoteMCP(t.Context(), Definition{ID: "remote-demo", DisplayName: "Remote Demo", Source: SourceCustom, Spec: spec}, Config{
		ID: "legacy-config", PluginID: "remote-demo", Scope: ScopeSystem, Payload: configPayload,
		CredentialRefs: json.RawMessage(`{"mcp_servers":{"main":{"bearer":{"name":"TOKEN_REF"}},"other":{"oauth_bundle":{"name":"OAUTH_REF"},"oauth_client_secret":{"name":"CLIENT_REF"}}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].Name != "remote-demo" || result[1].Name != "remote-demo-other" {
		t.Fatalf("result names=%+v", result)
	}
	if result[0].Evidence.OldCredential != "TOKEN_REF" || result[1].Evidence.OldCredential != "OAUTH_REF" {
		t.Fatalf("credential evidence=%+v", result)
	}
	if result[0].File.Path != "mcp/remote-demo.json" || result[1].File.Path != "mcp/remote-demo-other.json" {
		t.Fatalf("paths=%+v", result)
	}
}

func loadMaterializedPackage(t *testing.T, files []ResourceFileEntry) (*agentpackage.Package, agentpackage.Diagnostics) {
	t.Helper()
	captured := make(map[string]ResourceFile, len(files))
	for _, file := range files {
		captured[file.Path] = file.ResourceFile
	}
	return agentpackage.LoadFS(resourceMapFS(captured))
}
