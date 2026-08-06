package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/providercred"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/server"
)

type providerCredentialTestCipher struct{}

func (providerCredentialTestCipher) EncryptSystem(plaintext string) (string, error) {
	return "test:" + base64.RawStdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (providerCredentialTestCipher) DecryptSystem(ciphertext string) (string, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "test:"))
	return string(decoded), err
}

type providerCredentialTestStore interface {
	providercred.Store
	agentaccess.ProviderReader
}

// enableProviderCredentialAPI wires the same narrow Management ports the
// production composition root uses, with a reversible test cipher. The default
// server intentionally has no credential service so 503 behavior remains testable.
func enableProviderCredentialAPI(t *testing.T, env *testEnv) providerCredentialTestStore {
	t.Helper()
	store, ok := env.store.(providerCredentialTestStore)
	if !ok {
		t.Fatal("test config store does not implement provider credential ports")
	}
	creds := providercred.NewService(store, providerCredentialTestCipher{})
	env.rebuild(t, func(deps *server.Deps) {
		deps.AgentManagement = agentaccess.NewManagement(
			deps.AgentAccess,
			env.store,
			env.authStore,
			deps.PoolManager,
			testUserDir{users: env.oidcStore},
			agent.NewAgentActivityStore(env.db),
			creds,
			store,
			slog.With("component", "agent-provider-credential-test"),
		)
	})
	return store
}

func createCredentialProvider(t *testing.T, env *testEnv, id, key string) {
	t.Helper()
	if err := env.store.CreateProvider(context.Background(), config.Provider{
		ID: id, Type: id, Name: id, APIKey: key, BaseURL: "https://" + id + ".example.test", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProvider(%q): %v", id, err)
	}
}

func credentialCreateBody(name string, credentials []map[string]string) map[string]any {
	body := map[string]any{
		"name": name, "model": "credential-provider/model", "enabled": true,
	}
	if credentials != nil {
		body["provider_credentials"] = credentials
	}
	return body
}

func createdAgentID(t *testing.T, rrBody []byte) string {
	t.Helper()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rrBody, &body); err != nil {
		t.Fatalf("decode created agent: %v", err)
	}
	if body.ID == "" {
		t.Fatalf("created agent has no id: %s", rrBody)
	}
	return body.ID
}

func assertNoSecret(t *testing.T, secret string, values ...string) {
	t.Helper()
	if secret == "" {
		return
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			if strings.Contains(typed, secret) {
				t.Fatalf("secret appeared in serialized value %q", typed)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if strings.Contains(key, secret) {
					t.Fatalf("secret appeared in response key %q", key)
				}
				walk(item)
			}
		}
	}
	for _, value := range values {
		if strings.Contains(value, secret) {
			t.Fatalf("secret appeared in raw output %q", value)
		}
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			walk(decoded)
		}
	}
}

func assertNoJSONFields(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode response for field check: %v", err)
	}
	blocked := make(map[string]struct{}, len(forbidden))
	for _, field := range forbidden {
		blocked[field] = struct{}{}
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if _, found := blocked[key]; found {
					t.Fatalf("response contains forbidden field %q: %s", key, body)
				}
				walk(item)
			}
		}
	}
	walk(decoded)
}

func TestAgentProviderCredentialsCreateMetadataRotationAndDelete(t *testing.T) {
	env := setupAdmin(t)
	store := enableProviderCredentialAPI(t, env)
	createCredentialProvider(t, env, "credential-provider", "global-fallback")
	createCredentialProvider(t, env, "credential-provider-two", "global-fallback-two")

	secretOne := "credential-secret-one"
	secretTwo := "credential-secret-two"
	for _, tc := range []struct {
		name        string
		credentials []map[string]string
		want        int
	}{
		{name: "zero", want: 0},
		{name: "one", credentials: []map[string]string{{"provider_id": "credential-provider", "api_key": secretOne}}, want: 1},
		{name: "multiple", credentials: []map[string]string{{"provider_id": "credential-provider", "api_key": secretOne}, {"provider_id": "credential-provider-two", "api_key": secretTwo}}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, env, http.MethodPost, "/api/agents", credentialCreateBody("credential "+tc.name, tc.credentials))
			if rr.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
			}
			assertNoSecret(t, secretOne, rr.Body.String())
			assertNoSecret(t, secretTwo, rr.Body.String())
			assertNoJSONFields(t, rr.Body.String(), "provider_credentials", "api_key", "api_key_enc")
			id := createdAgentID(t, rr.Body.Bytes())
			get := doRequest(t, env, http.MethodGet, "/api/agents/"+id, nil)
			if get.Code != http.StatusOK {
				t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
			}
			assertNoSecret(t, secretOne, get.Body.String())
			assertNoSecret(t, secretTwo, get.Body.String())
			assertNoJSONFields(t, get.Body.String(), "provider_credentials", "api_key", "api_key_enc")
			list := doRequest(t, env, http.MethodGet, "/api/agents", nil)
			if list.Code != http.StatusOK {
				t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
			}
			assertNoSecret(t, secretOne, list.Body.String())
			assertNoSecret(t, secretTwo, list.Body.String())
			assertNoJSONFields(t, list.Body.String(), "provider_credentials", "api_key", "api_key_enc")

			meta := doRequest(t, env, http.MethodGet, "/api/agents/"+id+"/provider-credentials", nil)
			if meta.Code != http.StatusOK {
				t.Fatalf("metadata status=%d body=%s", meta.Code, meta.Body.String())
			}
			assertNoSecret(t, secretOne, meta.Body.String())
			assertNoSecret(t, secretTwo, meta.Body.String())
			assertNoJSONFields(t, meta.Body.String(), "api_key", "api_key_enc", "provider_id")
			var out struct {
				ProviderCredentials []struct {
					ID        string `json:"id"`
					HasAPIKey bool   `json:"has_api_key"`
					UpdatedAt string `json:"updated_at"`
				} `json:"provider_credentials"`
			}
			if err := json.Unmarshal(meta.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode metadata: %v", err)
			}
			if len(out.ProviderCredentials) != tc.want {
				t.Fatalf("metadata count=%d want %d: %s", len(out.ProviderCredentials), tc.want, meta.Body.String())
			}
			for _, item := range out.ProviderCredentials {
				if !item.HasAPIKey {
					t.Fatalf("metadata for %q reports no key", item.ID)
				}
				parsed, err := time.Parse(time.RFC3339, item.UpdatedAt)
				if err != nil || parsed.Location() != time.UTC {
					t.Fatalf("updated_at=%q is not UTC RFC3339: %v", item.UpdatedAt, err)
				}
			}
			if tc.want > 0 {
				item := doRequest(t, env, http.MethodGet, "/api/agents/"+id+"/provider-credentials/"+out.ProviderCredentials[0].ID, nil)
				if item.Code != http.StatusOK {
					t.Fatalf("credential GET status=%d body=%s", item.Code, item.Body.String())
				}
				assertNoSecret(t, secretOne, item.Body.String())
				assertNoSecret(t, secretTwo, item.Body.String())
				assertNoJSONFields(t, item.Body.String(), "api_key", "api_key_enc", "provider_id")
			}
		})
	}

	paged := doRequest(t, env, http.MethodGet, "/api/agents/credential-multiple/provider-credentials?page_size=1", nil)
	if paged.Code != http.StatusOK {
		t.Fatalf("paged metadata status=%d body=%s", paged.Code, paged.Body.String())
	}
	var firstPage struct {
		ProviderCredentials []any  `json:"provider_credentials"`
		NextPageToken       string `json:"next_page_token"`
		TotalSize           int    `json:"total_size"`
	}
	if err := json.Unmarshal(paged.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(firstPage.ProviderCredentials) != 1 || firstPage.NextPageToken == "" || firstPage.TotalSize != 2 {
		t.Fatalf("unexpected first page: %+v", firstPage)
	}
	second := doRequest(t, env, http.MethodGet, "/api/agents/credential-multiple/provider-credentials?page_size=1&page_token="+firstPage.NextPageToken, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPage struct {
		ProviderCredentials []any   `json:"provider_credentials"`
		NextPageToken       *string `json:"next_page_token"`
		TotalSize           int     `json:"total_size"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondPage.ProviderCredentials) != 1 || secondPage.NextPageToken != nil || secondPage.TotalSize != 2 {
		t.Fatalf("unexpected second page: %+v", secondPage)
	}
	for _, path := range []string{
		"/api/agents/credential-multiple/provider-credentials?page_size=0",
		"/api/agents/credential-multiple/provider-credentials?page_token=not-a-token",
	} {
		invalid := doRequest(t, env, http.MethodGet, path, nil)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid pagination %s status=%d body=%s", path, invalid.Code, invalid.Body.String())
		}
	}

	rr := doRequest(t, env, http.MethodPost, "/api/agents", credentialCreateBody("rotate credential", []map[string]string{{"provider_id": "credential-provider", "api_key": secretOne}}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create rotation agent: %d %s", rr.Code, rr.Body.String())
	}
	id := createdAgentID(t, rr.Body.Bytes())
	patchSecret := "patch-must-not-mutate-credentials"
	patch := doRequest(t, env, http.MethodPatch, "/api/agents/"+id, map[string]any{
		"name": "rotate credential", "model": "credential-provider/model", "enabled": true,
		"provider_credentials": []map[string]string{{"provider_id": "credential-provider", "api_key": patchSecret}},
	})
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status=%d body=%s", patch.Code, patch.Body.String())
	}
	assertNoSecret(t, patchSecret, patch.Body.String())
	assertNoJSONFields(t, patch.Body.String(), "provider_credentials", "api_key", "api_key_enc")
	unchanged, found, err := store.GetAgentProviderCredential(context.Background(), id, "credential-provider")
	if err != nil || !found {
		t.Fatalf("read credential after PATCH: found=%v err=%v", found, err)
	}
	decrypted, err := (providerCredentialTestCipher{}).DecryptSystem(unchanged.APIKeyEnc)
	if err != nil || decrypted != secretOne {
		t.Fatal("ordinary Agent PATCH mutated provider credentials")
	}
	rotated := "credential-secret-rotated"
	patched := doRequest(t, env, http.MethodPatch, "/api/agents/"+id+"/provider-credentials/credential-provider", map[string]string{"api_key": rotated})
	if patched.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", patched.Code, patched.Body.String())
	}
	assertNoSecret(t, rotated, patched.Body.String())
	assertNoJSONFields(t, patched.Body.String(), "api_key", "api_key_enc", "provider_id")
	var patchedMetadata struct {
		ID        string `json:"id"`
		HasAPIKey bool   `json:"has_api_key"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &patchedMetadata); err != nil {
		t.Fatalf("decode rotated metadata: %v", err)
	}
	if patchedMetadata.ID != "credential-provider" || !patchedMetadata.HasAPIKey {
		t.Fatalf("unexpected rotated metadata: %+v", patchedMetadata)
	}
	if parsed, err := time.Parse(time.RFC3339, patchedMetadata.UpdatedAt); err != nil || parsed.Location() != time.UTC {
		t.Fatalf("rotated updated_at=%q is not UTC RFC3339: %v", patchedMetadata.UpdatedAt, err)
	}
	stored, found, err := store.GetAgentProviderCredential(context.Background(), id, "credential-provider")
	if err != nil || !found {
		t.Fatalf("read rotated credential: found=%v err=%v", found, err)
	}
	if strings.Contains(stored.APIKeyEnc, rotated) {
		t.Fatal("ciphertext contains submitted key")
	}
	decrypted, err = (providerCredentialTestCipher{}).DecryptSystem(stored.APIKeyEnc)
	if err != nil || decrypted != rotated {
		t.Fatalf("rotated key was not persisted")
	}

	deleted := doRequest(t, env, http.MethodDelete, "/api/agents/"+id+"/provider-credentials/credential-provider", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	_, found, err = store.GetAgentProviderCredential(context.Background(), id, "credential-provider")
	if err != nil || found {
		t.Fatalf("deleted override remains: found=%v err=%v", found, err)
	}
	snapshot, err := env.store.Snapshot(context.Background(), id)
	if err != nil {
		t.Fatalf("snapshot after delete: %v", err)
	}
	if got := snapshot.Providers["credential-provider"].APIKey; got != "global-fallback" {
		t.Fatalf("delete did not restore global fallback: got %q", got)
	}
}

func TestAgentProviderCredentialValidationAndAuthorization(t *testing.T) {
	env := setupAdmin(t)
	enableProviderCredentialAPI(t, env)
	createCredentialProvider(t, env, "canonical-provider", "global")
	if err := env.store.CreateProvider(context.Background(), config.Provider{
		ID: "alias-canonical", Type: "alias", Name: "alias", APIKey: "global", BaseURL: "https://alias.example.test", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProvider(alias-canonical): %v", err)
	}

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"empty provider", credentialCreateBody("empty provider", []map[string]string{{"provider_id": "", "api_key": "safe-key"}})},
		{"empty key", credentialCreateBody("empty key", []map[string]string{{"provider_id": "canonical-provider", "api_key": ""}})},
		{"duplicate provider", credentialCreateBody("duplicate provider", []map[string]string{{"provider_id": "canonical-provider", "api_key": "first-key"}, {"provider_id": "canonical-provider", "api_key": "second-key"}})},
		{"unknown provider", credentialCreateBody("unknown provider", []map[string]string{{"provider_id": "missing-provider", "api_key": "unknown-key"}})},
		// A Provider type alias is not a canonical row ID and must not become durable.
		{"alias provider", credentialCreateBody("alias provider", []map[string]string{{"provider_id": "alias", "api_key": "alias-key"}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, env, http.MethodPost, "/api/agents", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rr.Code, rr.Body.String())
			}
			for _, credential := range tc.body["provider_credentials"].([]map[string]string) {
				assertNoSecret(t, credential["api_key"], rr.Body.String())
			}
		})
	}

	owner, ownerSession := newNonAdmin(t, env, "credential-owner")
	ownerAgent := createAgentAsUser(t, env, ownerSession, "credential-owned-agent")
	ownerPatch := doRequestWithSession(t, env.srv, ownerSession, http.MethodPatch, "/api/agents/"+ownerAgent+"/provider-credentials/canonical-provider", map[string]string{"api_key": "owner-key"})
	if ownerPatch.Code != http.StatusOK {
		t.Fatalf("creator PATCH status=%d body=%s", ownerPatch.Code, ownerPatch.Body.String())
	}
	adminPatch := doRequest(t, env, http.MethodPatch, "/api/agents/"+ownerAgent+"/provider-credentials/canonical-provider", map[string]string{"api_key": "admin-key"})
	if adminPatch.Code != http.StatusOK {
		t.Fatalf("admin PATCH status=%d body=%s", adminPatch.Code, adminPatch.Body.String())
	}
	assigned, assignedSession := newNonAdmin(t, env, "credential-assigned")
	if err := env.authStore.AssignAgent(context.Background(), assigned.ID, ownerAgent); err != nil {
		t.Fatalf("assign non-creator: %v", err)
	}
	metadata := doRequestWithSession(t, env.srv, assignedSession, http.MethodGet, "/api/agents/"+ownerAgent+"/provider-credentials", nil)
	if metadata.Code != http.StatusOK {
		t.Fatalf("assigned GET status=%d body=%s", metadata.Code, metadata.Body.String())
	}
	denied := doRequestWithSession(t, env.srv, assignedSession, http.MethodPatch, "/api/agents/"+ownerAgent+"/provider-credentials/canonical-provider", map[string]string{"api_key": "assigned-key"})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("assigned PATCH status=%d want 403 body=%s", denied.Code, denied.Body.String())
	}
	assertNoSecret(t, "assigned-key", denied.Body.String())

	missing := doRequest(t, env, http.MethodPatch, "/api/agents/missing-agent/provider-credentials/canonical-provider", map[string]string{"api_key": "missing-key"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent PATCH status=%d want 404 body=%s", missing.Code, missing.Body.String())
	}
	assertNoSecret(t, "missing-key", missing.Body.String())
	emptyPatch := doRequest(t, env, http.MethodPatch, "/api/agents/"+ownerAgent+"/provider-credentials/canonical-provider", map[string]string{"api_key": ""})
	if emptyPatch.Code != http.StatusBadRequest {
		t.Fatalf("empty PATCH status=%d want 400 body=%s", emptyPatch.Code, emptyPatch.Body.String())
	}
	aliasPatch := doRequest(t, env, http.MethodPatch, "/api/agents/"+ownerAgent+"/provider-credentials/alias", map[string]string{"api_key": "alias-path-key"})
	if aliasPatch.Code != http.StatusBadRequest {
		t.Fatalf("alias PATCH status=%d want 400 body=%s", aliasPatch.Code, aliasPatch.Body.String())
	}
	aliasDelete := doRequest(t, env, http.MethodDelete, "/api/agents/"+ownerAgent+"/provider-credentials/alias", nil)
	if aliasDelete.Code != http.StatusBadRequest {
		t.Fatalf("alias DELETE status=%d want 400 body=%s", aliasDelete.Code, aliasDelete.Body.String())
	}
	missingCredential := doRequest(t, env, http.MethodGet, "/api/agents/"+ownerAgent+"/provider-credentials/alias-canonical", nil)
	if missingCredential.Code != http.StatusNotFound {
		t.Fatalf("missing credential GET status=%d want 404 body=%s", missingCredential.Code, missingCredential.Body.String())
	}
	_ = owner // documents that the persisted creator, not assignment, authorizes mutation.
}

func TestAgentProviderCredentialsUnavailableNeverLeaksInput(t *testing.T) {
	env := setupAdmin(t) // default Management intentionally has no credential service
	secret := "unavailable-secret"
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/agents", credentialCreateBody("unavailable", []map[string]string{{"provider_id": "provider", "api_key": secret}})},
		{http.MethodGet, "/api/agents/missing/provider-credentials", nil},
		{http.MethodPatch, "/api/agents/missing/provider-credentials/provider", map[string]string{"api_key": secret}},
		{http.MethodDelete, "/api/agents/missing/provider-credentials/provider", nil},
	} {
		rr := doRequest(t, env, tc.method, tc.path, tc.body)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status=%d want 503 body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
		assertNoSecret(t, secret, rr.Body.String())
	}
}
