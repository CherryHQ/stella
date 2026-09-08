package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	"github.com/CherryHQ/stella/internal/server"
)

func setupPackageHTTPService(t *testing.T) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	contentStore, err := plugin.NewContentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plugins := plugin.NewService(
		env.db,
		env.deps.AgentAccess,
		plugin.NewCatalog(),
		mcp.NewMCPBackendPolicy(mcp.EndpointPolicy{}),
		func(_ context.Context, fn func() error) error { return fn() },
		plugin.WithContentStore(contentStore),
	)
	env.rebuild(t, func(d *server.Deps) { d.PluginService = plugins })
	return env
}

func TestPluginPackageHTTPImportAndCASUpdate(t *testing.T) {
	env := setupPackageHTTPService(t)
	firstSource := packageHTTPFixture(t, "one")
	createdResponse := doRequest(t, env, http.MethodPost, "/api/plugins/import", map[string]any{
		"source_path": firstSource,
		"initial_config": map[string]any{
			"scope": "system", "is_enabled": false,
			"config": map[string]any{"binaries": map[string]any{"fd": map[string]any{"version": "10.4.2"}}},
		},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created apitypes.CreatePluginResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if created.Plugin.Id != "http.package" || created.Plugin.Spec["origin"] != "package" {
		t.Fatalf("imported plugin = %#v, want package http.package", created.Plugin)
	}
	if created.Config.Id.String() == "" {
		t.Fatal("import returned an empty config ID")
	}
	var importedConfigID string
	var importedConfigPayload []byte
	if err := env.db.QueryRow(t.Context(), `SELECT id::text, config FROM plugin_config WHERE plugin_id = $1 AND scope = 'system'`, created.Plugin.Id).Scan(&importedConfigID, &importedConfigPayload); err != nil {
		t.Fatalf("read imported config: %v", err)
	}
	if importedConfigID != created.Config.Id.String() {
		t.Fatalf("persisted config ID = %q, response = %q", importedConfigID, created.Config.Id)
	}

	secondSource := packageHTTPFixture(t, "two")
	previewResponse := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update/preview", map[string]any{
		"source_path":       secondSource,
		"expected_revision": created.Plugin.Revision,
	})
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview apitypes.PreviewPluginPackageResponse
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	updatedResponse := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update", map[string]any{
		"source_path":             secondSource,
		"expected_revision":       created.Plugin.Revision,
		"expected_package_digest": preview.CandidateDigest,
	})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated apitypes.PluginDefinition
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Revision == nil || created.Plugin.Revision == nil || *updated.Revision != *created.Plugin.Revision+1 {
		t.Fatalf("updated revision = %v, initial = %v", updated.Revision, created.Plugin.Revision)
	}
	var afterUpdateConfigID string
	var afterUpdateConfigPayload []byte
	if err := env.db.QueryRow(t.Context(), `SELECT id::text, config FROM plugin_config WHERE plugin_id = $1 AND scope = 'system'`, created.Plugin.Id).Scan(&afterUpdateConfigID, &afterUpdateConfigPayload); err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	if afterUpdateConfigID != importedConfigID {
		t.Fatalf("config ID changed across definition update: %q -> %q", importedConfigID, afterUpdateConfigID)
	}
	if !bytes.Equal(importedConfigPayload, afterUpdateConfigPayload) || !bytes.Contains(afterUpdateConfigPayload, []byte(`"10.4.2"`)) {
		t.Fatalf("config pin changed across definition update: before=%s after=%s", importedConfigPayload, afterUpdateConfigPayload)
	}

	staleResponse := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update", map[string]any{
		"source_path":             secondSource,
		"expected_revision":       *created.Plugin.Revision,
		"expected_package_digest": preview.CandidateDigest,
	})
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale update = %d: %s, want %d", staleResponse.Code, staleResponse.Body.String(), http.StatusConflict)
	}
}

func TestPluginPackageHTTPPreviewPinsCandidateBeforeUpdate(t *testing.T) {
	env := setupPackageHTTPService(t)
	firstSource := packageHTTPFixture(t, "one")
	createdResponse := doRequest(t, env, http.MethodPost, "/api/plugins/import", map[string]any{
		"source_path":    firstSource,
		"initial_config": map[string]any{"scope": "system", "is_enabled": false},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created apitypes.CreatePluginResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode import response: %v", err)
	}

	candidate := packageHTTPFixture(t, "candidate")
	previewResponse := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update/preview", map[string]any{
		"source_path": candidate, "expected_revision": created.Plugin.Revision,
	})
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview apitypes.PreviewPluginPackageResponse
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.CandidateDigest == "" || !strings.HasPrefix(preview.CandidateDigest, "sha256:") {
		t.Fatalf("preview digest = %q", preview.CandidateDigest)
	}
	if strings.Contains(previewResponse.Body.String(), candidate) {
		t.Fatalf("preview leaked source path: %s", previewResponse.Body.String())
	}

	if err := os.WriteFile(filepath.Join(candidate, "plugin.json"), []byte(`{"$schema":"`+agentpackage.PluginSchemaV1+`","name":"http.package","version":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	staleCandidate := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update", map[string]any{
		"source_path": candidate, "expected_revision": created.Plugin.Revision,
		"expected_package_digest": preview.CandidateDigest,
	})
	if staleCandidate.Code != http.StatusConflict {
		t.Fatalf("changed candidate update = %d: %s, want %d", staleCandidate.Code, staleCandidate.Body.String(), http.StatusConflict)
	}

	previewResponse = doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update/preview", map[string]any{
		"source_path": candidate, "expected_revision": created.Plugin.Revision,
	})
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("second preview = %d: %s", previewResponse.Code, previewResponse.Body.String())
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode second preview: %v", err)
	}
	updated := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update", map[string]any{
		"source_path": candidate, "expected_revision": created.Plugin.Revision,
		"expected_package_digest": preview.CandidateDigest,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("pinned update = %d: %s", updated.Code, updated.Body.String())
	}
}

func TestPluginPackageHTTPPreviewRejectsOversizedCandidate(t *testing.T) {
	env := setupPackageHTTPService(t)
	createdResponse := doRequest(t, env, http.MethodPost, "/api/plugins/import", map[string]any{
		"source_path":    packageHTTPFixture(t, "one"),
		"initial_config": map[string]any{"scope": "system", "is_enabled": false},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	oversized := t.TempDir()
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"http.package","description":"` + strings.Repeat("x", 256<<10) + `"}`
	if err := os.WriteFile(filepath.Join(oversized, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update/preview", map[string]any{
		"source_path": oversized, "expected_revision": 1,
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized candidate preview = %d: %s, want %d", response.Code, response.Body.String(), http.StatusBadRequest)
	}
}

func TestPluginPackageHTTPPreviewRejectsUnsupportedOAuthConnectionWithoutConfigs(t *testing.T) {
	env := setupPackageHTTPService(t)
	createdResponse := doRequest(t, env, http.MethodPost, "/api/plugins/import", map[string]any{
		"source_path":    packageHTTPFixture(t, "one"),
		"initial_config": map[string]any{"scope": "system", "is_enabled": false},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created apitypes.CreatePluginResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	deleted := doRequest(t, env, http.MethodDelete, "/api/plugins/http.package/configs/"+created.Config.Id.String()+"?expected_revision=1", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete config = %d: %s", deleted.Code, deleted.Body.String())
	}
	candidate := packageHTTPFixtureWithOAuthConnection(t)
	preview := doRequest(t, env, http.MethodPost, "/api/plugins/http.package/update/preview", map[string]any{
		"source_path": candidate, "expected_revision": created.Plugin.Revision,
	})
	if preview.Code != http.StatusBadRequest {
		t.Fatalf("unsupported OAuth connection preview = %d: %s, want %d", preview.Code, preview.Body.String(), http.StatusBadRequest)
	}
	if strings.Contains(preview.Body.String(), "connection-secret") {
		t.Fatalf("preview leaked OAuth connection: %s", preview.Body.String())
	}
}

func TestPluginPackageHTTPRejectsNonAdminBeforeReadingSource(t *testing.T) {
	env := setupPackageHTTPService(t)
	_, token := newNonAdmin(t, env, "package-http-user")
	missingSource := filepath.Join(t.TempDir(), "does-not-exist")

	for name, test := range map[string]struct {
		path string
		body map[string]any
	}{
		"import": {
			path: "/api/plugins/import",
			body: map[string]any{
				"source_path":    missingSource,
				"initial_config": map[string]any{"scope": "system", "is_enabled": false},
			},
		},
		"update": {
			path: "/api/plugins/missing/update",
			body: map[string]any{"source_path": missingSource, "expected_revision": 1, "expected_package_digest": "sha256:" + strings.Repeat("0", 64)},
		},
		"preview": {
			path: "/api/plugins/missing/update/preview",
			body: map[string]any{"source_path": missingSource, "expected_revision": 1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := doRequestWithSession(t, env.srv, token, http.MethodPost, test.path, test.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("non-admin %s = %d: %s, want %d", name, response.Code, response.Body.String(), http.StatusForbidden)
			}
		})
	}
}

func TestPluginPackageHTTPRejectsOversizedBody(t *testing.T) {
	env := setupPackageHTTPService(t)
	response := doRequest(t, env, http.MethodPost, "/api/plugins/import", map[string]any{
		"source_path":    strings.Repeat("x", 1<<20),
		"initial_config": map[string]any{"scope": "system", "is_enabled": false},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized import = %d: %s, want %d", response.Code, response.Body.String(), http.StatusBadRequest)
	}
}

func packageHTTPFixture(t *testing.T, description string) string {
	t.Helper()
	root := t.TempDir()
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"http.package","description":"` + description + `","extensions":{"com.cherryhq.stella":{"version":"1","binaries":[{"name":"fd","tool":"fd","version":"10.4.2"}]}}}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func packageHTTPFixtureWithOAuthConnection(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"http.package","version":"2","extensions":{"com.cherryhq.stella":{"version":"1","oauth":[{"provider":"github","bindings":[{"credential":"access_token","connection":"connection-secret"}]}]}}}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
