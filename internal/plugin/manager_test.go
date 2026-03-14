package plugin

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestNewManager(t *testing.T) {
	m := NewManager(discardLogger(), []string{"read"})
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.registry == nil {
		t.Fatal("expected non-nil registry")
	}
	if m.logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestLoadAllEmpty(t *testing.T) {
	pluginapi.ResetFactories()
	m := NewManager(discardLogger(), nil)

	err := m.LoadAll(nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	err = m.LoadAll([]config.PluginConfig{})
	if err != nil {
		t.Fatalf("expected nil error for empty slice, got: %v", err)
	}
}

func TestLoadAllInvalidPath(t *testing.T) {
	pluginapi.ResetFactories()
	m := NewManager(discardLogger(), nil)

	err := m.LoadAll([]config.PluginConfig{
		{Path: "/nonexistent/path/to/plugin.js", Config: nil},
	})
	if err != nil {
		t.Fatalf("invalid path should not fail LoadAll, got: %v", err)
	}
}

func TestCloseReverseOrder(t *testing.T) {
	m := NewManager(discardLogger(), nil)

	var closeOrder []string

	for _, name := range []string{"first", "second", "third"} {
		n := name
		m.plugins = append(m.plugins, &mockPlugin{
			name: n,
			closeFn: func() error {
				closeOrder = append(closeOrder, n)
				return nil
			},
		})
	}

	if err := m.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"third", "second", "first"}
	if len(closeOrder) != len(expected) {
		t.Fatalf("expected %d closes, got %d", len(expected), len(closeOrder))
	}
	for i, name := range expected {
		if closeOrder[i] != name {
			t.Errorf("close[%d] = %q, want %q", i, closeOrder[i], name)
		}
	}
}

func TestDetectKindJS(t *testing.T) {
	tmp := t.TempDir()
	jsFile := filepath.Join(tmp, "plugin.js")
	if err := os.WriteFile(jsFile, []byte("// js plugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectKind(jsFile)
	if got != "js" {
		t.Errorf("detectKind(%q) = %q, want %q", jsFile, got, "js")
	}
}

func TestDetectKindGo(t *testing.T) {
	tmp := t.TempDir()
	goDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module myplugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectKind(goDir)
	if got != "go" {
		t.Errorf("detectKind(%q) = %q, want %q", goDir, got, "go")
	}
}

func TestDetectKindUnknown(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"nonexistent", "/nonexistent/path/file.txt"},
		{"directory without go.mod", t.TempDir()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectKind(tt.path)
			if got != "" {
				t.Errorf("detectKind(%q) = %q, want empty string", tt.path, got)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"~/a/b/c.js", filepath.Join(home, "a/b/c.js")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandPath(tt.input)
			if got != tt.want {
				t.Errorf("expandPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// mockPlugin implements pluginapi.Plugin for testing.
type mockPlugin struct {
	name    string
	closeFn func() error
}

func (p *mockPlugin) Name() string                   { return p.name }
func (p *mockPlugin) Init(_ pluginapi.Context) error { return nil }
func (p *mockPlugin) Close() error {
	if p.closeFn != nil {
		return p.closeFn()
	}
	return nil
}

// Ensure mockPlugin implements the Plugin interface.
var _ pluginapi.Plugin = (*mockPlugin)(nil)

func TestCloseReturnsLastError(t *testing.T) {
	m := NewManager(discardLogger(), nil)
	m.plugins = append(m.plugins,
		&mockPlugin{name: "ok", closeFn: func() error { return nil }},
		&mockPlugin{name: "fail", closeFn: func() error { return fmt.Errorf("close error") }},
	)

	err := m.Close()
	if err == nil {
		t.Fatal("expected error from Close")
	}
	if err.Error() != "close error" {
		t.Errorf("error = %q, want %q", err.Error(), "close error")
	}
}
