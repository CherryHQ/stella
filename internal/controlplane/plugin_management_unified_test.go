package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	pluginapi "github.com/CherryHQ/stella/internal/plugin"
)

func TestUnifiedPluginManagementHandlerRequiresUnifiedAccess(t *testing.T) {
	h := NewUnifiedPluginManagementHandler(nil)
	if _, err := h.List(t.Context(), SettingsPluginListInput{}); !errors.Is(err, pluginapi.ErrForbidden) {
		t.Fatalf("nil unified plugin access = %v, want forbidden", err)
	}
}

func pluginListToolSpec(t *testing.T) SettingsPluginActionTool {
	t.Helper()
	for _, spec := range SettingsPluginActionTools() {
		if spec.Action == "list" {
			return spec
		}
	}
	t.Fatal("settings_plugin list spec missing")
	return SettingsPluginActionTool{}
}

func TestPluginManagementToolRejectsUnauthenticatedBeforeBindingService(t *testing.T) {
	called := false
	tool := NewPluginManagementTool(pluginListToolSpec(t), func() *pluginapi.FileService {
		called = true
		return nil
	})
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil || !errors.Is(err, authz.ErrUnauthenticated) && !strings.Contains(err.Error(), "no user identity") {
		t.Fatalf("unauthenticated plugin tool error = %v", err)
	}
	if called {
		t.Fatal("plugin file service was resolved before DirectAuthority")
	}
}
