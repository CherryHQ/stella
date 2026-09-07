package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/server"
)

func TestNativePluginHandlers(t *testing.T) {
	env := setupAdmin(t)
	policy := plugin.NewNativePolicy(cfgstore.NewDBStore(env.db), plugin.NativeRegistryMap{
		"channel/telegram": true,
		"system/email":     false,
	})
	mutations := 0
	policy.SetMutationFence(func(ctx context.Context, mutate func() error) error {
		mutations++
		return env.deps.PoolManager.ApplyPluginMutation(ctx, mutate)
	})
	env.rebuild(t, func(deps *server.Deps) { deps.NativePolicy = policy })
	for _, id := range []string{"native-a", "native-b"} {
		if err := env.store.CreateAgent(t.Context(), config.Agent{
			ID: id, Name: id, Model: "test", Scope: config.AgentScopeSystem, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "native-user", auth.RoleUser)
	const base = "/api/native-plugins/channel/telegram"
	const denials = base + "/agent-denials"
	check := func(t *testing.T, rr *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rr.Code != want {
			t.Fatalf("status = %d, want %d: %s", rr.Code, want, rr.Body.String())
		}
	}

	t.Run("all routes require administrator", func(t *testing.T) {
		for _, request := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/native-plugins", nil},
			{http.MethodGet, base, nil},
			{http.MethodPatch, base, map[string]any{"is_enabled": false}},
			{http.MethodGet, denials, nil},
			{http.MethodPost, denials, map[string]any{"agent_id": "native-a"}},
			{http.MethodGet, denials + "/native-a", nil},
			{http.MethodDelete, denials + "/native-a", nil},
		} {
			check(t, doUnauthRequest(t, env.srv, request.method, request.path, request.body), http.StatusUnauthorized)
			check(t, doRequestWithSession(t, env.srv, userToken, request.method, request.path, request.body), http.StatusForbidden)
		}
		if mutations != 0 {
			t.Fatalf("unauthorized requests entered mutation fence %d times", mutations)
		}
	})

	t.Run("registered catalog and pagination", func(t *testing.T) {
		rr := doRequest(t, env, http.MethodGet, "/api/native-plugins?page_size=1", nil)
		check(t, rr, http.StatusOK)
		var first apitypes.NativePluginList
		if err := json.Unmarshal(parseResponse(t, rr).Data, &first); err != nil {
			t.Fatal(err)
		}
		if len(first.NativePlugins) != 1 || first.NativePlugins[0].Id != "channel/telegram" || first.NextPageToken == nil {
			t.Fatalf("first page = %#v", first)
		}
		rr = doRequest(t, env, http.MethodGet, "/api/native-plugins?page_size=1&page_token="+url.QueryEscape(*first.NextPageToken), nil)
		check(t, rr, http.StatusOK)
		var second apitypes.NativePluginList
		if err := json.Unmarshal(parseResponse(t, rr).Data, &second); err != nil {
			t.Fatal(err)
		}
		if len(second.NativePlugins) != 1 || second.NativePlugins[0].Id != "system/email" || second.NativePlugins[0].IsEnabled || second.NextPageToken != nil {
			t.Fatalf("second page = %#v", second)
		}
		for _, path := range []string{"/api/native-plugins", denials} {
			for _, query := range []string{"?page_size=0", "?page_size=501", "?page_token=invalid"} {
				check(t, doRequest(t, env, http.MethodGet, path+query, nil), http.StatusBadRequest)
			}
		}
		check(t, doRequest(t, env, http.MethodGet, "/api/native-plugins/custom/telegram", nil), http.StatusNotFound)
	})

	t.Run("strict JSON does not mutate policy", func(t *testing.T) {
		before := mutations
		for _, request := range []struct{ method, path, body string }{
			{http.MethodPatch, base, `{}`},
			{http.MethodPatch, base, `null`},
			{http.MethodPatch, base, `{"is_enabled":null}`},
			{http.MethodPatch, base, `{"is_enabled":"false"}`},
			{http.MethodPatch, base, `{"is_enabled":false,"config":{}}`},
			{http.MethodPatch, base, `{"is_enabled":false}{}`},
			{http.MethodPost, denials, `{}`},
			{http.MethodPost, denials, `{"agent_id":null}`},
			{http.MethodPost, denials, `{"agent_id":" "}`},
			{http.MethodPost, denials, `{"agent_id":"native-a","is_denied":false}`},
			{http.MethodPost, denials, `{"agent_id":"native-a"}{}`},
			{http.MethodPost, denials, `{"agent_id":"` + strings.Repeat("x", 17<<10) + `"}`},
		} {
			req := httptest.NewRequestWithContext(t.Context(), request.method, request.path, strings.NewReader(request.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: env.bearerToken})
			rr := httptest.NewRecorder()
			env.srv.Handler().ServeHTTP(rr, req)
			check(t, rr, http.StatusBadRequest)
		}
		if mutations != before {
			t.Fatalf("invalid bodies entered mutation fence: %d -> %d", before, mutations)
		}
	})

	t.Run("global and per-agent caps compose", func(t *testing.T) {
		check(t, doRequest(t, env, http.MethodPatch, base, map[string]any{"is_enabled": true}), http.StatusOK)
		assertAllowed := func(agentID string, want bool) {
			t.Helper()
			got, err := policy.Allows(t.Context(), "channel/telegram", agentID)
			if err != nil || got != want {
				t.Fatalf("admission %s = %v, %v; want %v", agentID, got, err, want)
			}
		}
		check(t, doRequest(t, env, http.MethodPost, denials, map[string]any{"agent_id": "missing-agent"}), http.StatusNotFound)
		for _, id := range []string{"native-a", "native-b"} {
			check(t, doRequest(t, env, http.MethodPost, denials, map[string]any{"agent_id": id}), http.StatusCreated)
		}
		pagePath := denials + "?page_size=1"
		for _, wantID := range []string{"native-a", "native-b"} {
			rr := doRequest(t, env, http.MethodGet, pagePath, nil)
			check(t, rr, http.StatusOK)
			var page apitypes.NativeAgentDenyList
			if err := json.Unmarshal(parseResponse(t, rr).Data, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Denials) != 1 || page.Denials[0].AgentId != wantID || !page.Denials[0].IsDenied {
				t.Fatalf("deny page = %#v, want %s", page, wantID)
			}
			if wantID == "native-a" {
				if page.NextPageToken == nil {
					t.Fatal("first deny page has no continuation")
				}
				pagePath = denials + "?page_size=1&page_token=" + url.QueryEscape(*page.NextPageToken)
			} else if page.NextPageToken != nil {
				t.Fatal("last deny page has a continuation")
			}
		}
		check(t, doRequest(t, env, http.MethodPost, denials, map[string]any{"agent_id": "native-a"}), http.StatusConflict)
		check(t, doRequest(t, env, http.MethodGet, denials+"/native-a", nil), http.StatusOK)
		assertAllowed("native-a", false)
		check(t, doRequest(t, env, http.MethodDelete, denials+"/native-b", nil), http.StatusNoContent)
		assertAllowed("native-b", true)
		check(t, doRequest(t, env, http.MethodPatch, base, map[string]any{"is_enabled": false}), http.StatusOK)
		assertAllowed("native-a", false)
		assertAllowed("native-b", false)
		check(t, doRequest(t, env, http.MethodDelete, denials+"/native-a", nil), http.StatusNoContent)
		assertAllowed("native-a", false)
		check(t, doRequest(t, env, http.MethodDelete, denials+"/native-a", nil), http.StatusNotFound)
		check(t, doRequest(t, env, http.MethodGet, denials+"/native-a", nil), http.StatusNotFound)
		check(t, doRequest(t, env, http.MethodPatch, base, map[string]any{"is_enabled": true}), http.StatusOK)
		assertAllowed("native-a", true)
		assertAllowed("native-b", true)
	})

	t.Run("unknown writes retain committed state and cross the fence", func(t *testing.T) {
		uncertain := plugin.NewNativePolicy(nativeUncertainWriteStore{NativeStore: cfgstore.NewDBStore(env.db)}, plugin.NativeRegistryMap{"channel/telegram": true})
		unknownOutcomes := 0
		uncertain.SetMutationFence(func(ctx context.Context, mutate func() error) error {
			err := env.deps.PoolManager.ApplyPluginMutation(ctx, mutate)
			if errors.Is(err, plugin.ErrCommitOutcomeUnknown) {
				unknownOutcomes++
			}
			return err
		})
		env.rebuild(t, func(deps *server.Deps) { deps.NativePolicy = uncertain })
		check(t, doRequest(t, env, http.MethodPatch, base, map[string]any{"is_enabled": false}), http.StatusInternalServerError)
		if enabled, err := policy.GlobalEnabled(t.Context(), "channel/telegram"); err != nil || enabled {
			t.Fatalf("committed global switch = %v, %v", enabled, err)
		}
		check(t, doRequest(t, env, http.MethodPost, denials, map[string]any{"agent_id": "native-a"}), http.StatusInternalServerError)
		if denied, err := policy.AgentDenied(t.Context(), "channel/telegram", "native-a"); err != nil || !denied {
			t.Fatalf("committed deny = %v, %v", denied, err)
		}
		check(t, doRequest(t, env, http.MethodDelete, denials+"/native-a", nil), http.StatusInternalServerError)
		if denied, err := policy.AgentDenied(t.Context(), "channel/telegram", "native-a"); err != nil || denied {
			t.Fatalf("committed deny deletion = %v, %v", denied, err)
		}
		if unknownOutcomes != 3 {
			t.Fatalf("unknown outcomes crossing the fence = %d, want 3", unknownOutcomes)
		}
	})

	t.Run("empty catalog is an array", func(t *testing.T) {
		env.rebuild(t, func(deps *server.Deps) {
			deps.NativePolicy = plugin.NewNativePolicy(cfgstore.NewDBStore(env.db), plugin.NativeRegistryMap{})
		})
		rr := doRequest(t, env, http.MethodGet, "/api/native-plugins", nil)
		check(t, rr, http.StatusOK)
		var page apitypes.NativePluginList
		if err := json.Unmarshal(parseResponse(t, rr).Data, &page); err != nil {
			t.Fatal(err)
		}
		if page.NativePlugins == nil || len(page.NativePlugins) != 0 || page.NextPageToken != nil {
			t.Fatalf("empty catalog must serialize an empty array: %s", rr.Body.String())
		}
	})
}

// Simulate a committed database write whose acknowledgement was lost. This
// tests the HTTP/fence seam without making a flaky network race a prerequisite.
type nativeUncertainWriteStore struct{ plugin.NativeStore }

func (s nativeUncertainWriteStore) SetNativePluginEnabled(ctx context.Context, id string, enabled bool) error {
	if err := s.NativeStore.SetNativePluginEnabled(ctx, id, enabled); err != nil {
		return err
	}
	return plugin.ErrCommitOutcomeUnknown
}

func (s nativeUncertainWriteStore) SetNativeAgentDeny(ctx context.Context, id, agentID string) error {
	if err := s.NativeStore.SetNativeAgentDeny(ctx, id, agentID); err != nil {
		return err
	}
	return plugin.ErrCommitOutcomeUnknown
}

func (s nativeUncertainWriteStore) DeleteNativeAgentDeny(ctx context.Context, id, agentID string) error {
	if err := s.NativeStore.DeleteNativeAgentDeny(ctx, id, agentID); err != nil {
		return err
	}
	return plugin.ErrCommitOutcomeUnknown
}
