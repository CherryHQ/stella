package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestWritePluginErrorMapsOAuthClientInitialization(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePluginError(recorder, mcp.ErrOAuthClientInitializationRequired)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("OAuth client initialization status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != http.StatusConflict {
		t.Fatalf("error code = %d, want %d", body.Error.Code, http.StatusConflict)
	}
	const want = "administrator must initialize this connection before users can authorize their own accounts"
	if body.Error.Message != want {
		t.Fatalf("error message = %q, want %q", body.Error.Message, want)
	}
}

func TestPluginFileAccessAuthenticationPrecedesUnavailableService(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	unauthenticated := httptest.NewRecorder()
	server.ListPlugins(unauthenticated, request, apiserver.ListPluginsParams{})
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	authenticatedRequest := request.WithContext(withAuthInfo(request.Context(), &AuthInfo{
		UserID: "user-1", Role: auth.RoleUser,
	}))
	serviceUnavailable := httptest.NewRecorder()
	server.ListPlugins(serviceUnavailable, authenticatedRequest, apiserver.ListPluginsParams{})
	if serviceUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable service status = %d, want %d", serviceUnavailable.Code, http.StatusServiceUnavailable)
	}
}

func TestPluginFileResourceViewProjectsSafeMetadata(t *testing.T) {
	resource := pluginpkg.FileResource{
		Key:            pluginpkg.ResourceKey{Scope: pluginpkg.ScopeSystem, Kind: pluginpkg.ResourcePlugin, Name: "demo"},
		Digest:         "sha256:content",
		SettingsDigest: "sha256:settings",
		Disabled:       true,
		Forbidden:      true,
		Diagnostics: agentpackage.Diagnostics{{
			Severity: agentpackage.SeverityWarning,
			Code:     "resource.warning",
			Message:  "safe diagnostic",
			Path:     "plugin.json",
		}},
	}
	view, err := pluginFileResourceView(resource, false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Id == nil || *view.Id != resource.Key.ID() {
		t.Fatalf("resource ID = %#v", view.Id)
	}
	if view.Name != "demo" || view.Scope != apitypes.PluginResourceScopeSystem {
		t.Fatalf("resource identity = %#v", view)
	}
	if view.IsEnabled || view.IsReadOnly == nil || !*view.IsReadOnly || view.IsForbidden == nil || !*view.IsForbidden {
		t.Fatalf("resource policy projection = %#v", view)
	}
	if view.SettingsDigest == nil || *view.SettingsDigest != "sha256:settings" || view.Diagnostics == nil || len(*view.Diagnostics) != 1 {
		t.Fatalf("resource metadata = %#v", view)
	}
}

func TestWritePluginErrorMapsFileResourceErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "scope", err: pluginpkg.ErrUnknownScope, want: http.StatusBadRequest},
		{name: "resource ID", err: pluginpkg.ErrInvalidResourceID, want: http.StatusBadRequest},
		{name: "cas", err: pluginpkg.ErrConflict, want: http.StatusConflict},
		{name: "forbidden", err: authz.ErrForbidden, want: http.StatusForbidden},
		{name: "not found", err: authz.ErrNotFound, want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writePluginError(recorder, tc.err)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

func TestPluginFileResourceViewDoesNotExposePrivateDiagnostics(t *testing.T) {
	resource := pluginpkg.FileResource{
		Key: pluginpkg.ResourceKey{Scope: pluginpkg.ScopeSystem, Kind: pluginpkg.ResourcePlugin, Name: "safe"},
		Diagnostics: agentpackage.Diagnostics{{
			Severity: agentpackage.SeverityError,
			Code:     "resource.capture",
			Message:  "content could not be captured",
			Path:     "plugin.json",
		}},
	}
	view, err := pluginFileResourceView(resource, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"vault://", "Authorization", "private.example"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("resource view exposed %q: %s", forbidden, encoded)
		}
	}
}
