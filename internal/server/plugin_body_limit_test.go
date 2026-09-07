package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

func TestPluginWritesBoundRequestBodyBeforeDecoding(t *testing.T) {
	svc := pluginpkg.NewService(dbtest.New(t), nil, pluginpkg.NewCatalog(), pluginpkg.BackendPolicy{}, nil)
	server := &Server{pluginSvc: svc}
	handlers := map[string]http.HandlerFunc{
		"create plugin": server.CreatePlugin,
		"update plugin": func(w http.ResponseWriter, r *http.Request) { server.UpdatePlugin(w, r, "demo") },
		"create config": func(w http.ResponseWriter, r *http.Request) { server.CreatePluginConfig(w, r, "demo") },
		"update config": func(w http.ResponseWriter, r *http.Request) { server.UpdatePluginConfig(w, r, "demo", "config") },
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			body := strings.NewReader(`{"description":"` + strings.Repeat("x", 2<<20) + `"}`)
			request := httptest.NewRequest(http.MethodPost, "/api/plugins", body)
			request = request.WithContext(withAuthInfo(request.Context(), &AuthInfo{UserID: "10000000-0000-0000-0000-000000000001", Role: auth.RoleUser}))
			response := httptest.NewRecorder()
			handler(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			if body.Len() < 1<<20 {
				t.Fatalf("oversized body was consumed beyond the 1 MiB ceiling: %d bytes left", body.Len())
			}
		})
	}
}
