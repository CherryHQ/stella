package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginapi"
)

func TestNewRegistryWithBindingsUsesRuntimePluginOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNA_HOME", home)

	pluginDir := filepath.Join(config.InstalledPluginsPath(), "replacement-read", "1.0.0")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	entrypoint := filepath.Join(pluginDir, "helper.sh")
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  method=$(printf '%s\n' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    handshake)
      printf '{"id":"%s","type":"response","result":{"protocol_version":"anna-plugin/v1","name":"replacement-read","version":"1.0.0","kind":"tool","capabilities":["tool.call","health.check","shutdown.graceful"],"tool":{"name":"read","description":"replacement read","input_schema":{"type":"object"}}}}\n' "$id"
      ;;
    health)
      printf '{"id":"%s","type":"response","result":{"ok":true}}\n' "$id"
      ;;
    call_tool)
      printf '{"id":"%s","type":"response","result":{"output":"replacement read"}}\n' "$id"
      ;;
    shutdown)
      printf '{"id":"%s","type":"response","result":{}}\n' "$id"
      exit 0
      ;;
    *)
      printf '{"id":"%s","type":"response","error":{"code":"unknown_method","message":"%s"}}\n' "$id" "$method"
      ;;
  esac
done
`
	if err := os.WriteFile(entrypoint, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := pluginapi.Manifest{
		Name:            "replacement-read",
		Version:         "1.0.0",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "helper.sh",
		Tool: &pluginapi.ToolSpec{
			Name:        "read",
			Description: "replacement read",
			InputSchema: map[string]any{"type": "object"},
		},
		Capabilities: []pluginapi.Capability{
			pluginapi.CapabilityToolCall,
			pluginapi.CapabilityHealthCheck,
			pluginapi.CapabilityGracefulShutdown,
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	sandbox := t.TempDir()
	allowedPath := filepath.Join(sandbox, "note.txt")
	if err := os.WriteFile(allowedPath, []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	bindings := config.DefaultRuntimePluginBindings()
	bindings.Tools["read"] = "tool/replacement-read"

	reg, err := agenttool.NewRegistryWithBindings(t.TempDir(), bindings, sandbox)
	if err != nil {
		t.Fatalf("NewRegistryWithBindings: %v", err)
	}
	defer func() { _ = reg.Close() }()

	got, err := reg.Execute(context.Background(), "read", map[string]any{"file_path": allowedPath})
	if err != nil {
		t.Fatalf("Execute(read): %v", err)
	}
	if got != "replacement read" {
		t.Fatalf("Execute(read) = %q, want replacement output", got)
	}
}

func TestNewRegistryWithBindingsRejectsWrongToolName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNA_HOME", home)

	pluginDir := filepath.Join(config.InstalledPluginsPath(), "bad-read", "1.0.0")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	entrypoint := filepath.Join(pluginDir, "helper.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := pluginapi.Manifest{
		Name:            "bad-read",
		Version:         "1.0.0",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "helper.sh",
		Tool: &pluginapi.ToolSpec{
			Name:        "other-tool",
			Description: "wrong replacement",
			InputSchema: map[string]any{"type": "object"},
		},
		Capabilities: []pluginapi.Capability{
			pluginapi.CapabilityToolCall,
			pluginapi.CapabilityHealthCheck,
			pluginapi.CapabilityGracefulShutdown,
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	bindings := config.DefaultRuntimePluginBindings()
	bindings.Tools["read"] = "tool/bad-read"

	_, err = agenttool.NewRegistryWithBindings(t.TempDir(), bindings, t.TempDir())
	if err == nil {
		t.Fatal("expected binding error")
	}
	if got := err.Error(); got != `tool read bound to plugin tool/bad-read exposing tool "other-tool"` {
		t.Fatalf("NewRegistryWithBindings error = %q", got)
	}
}
