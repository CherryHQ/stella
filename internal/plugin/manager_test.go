package plugin

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/config"
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

	for _, name := range []string{"first", "second", "third"} {
		m.plugins = append(m.plugins, &JSPlugin{name: name})
	}

	// JSPlugin.Close is safe to call with nil runtime. Verify no panic.
	if err := m.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsJSPlugin(t *testing.T) {
	tmp := t.TempDir()
	jsFile := filepath.Join(tmp, "plugin.js")
	if err := os.WriteFile(jsFile, []byte("// js plugin"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !isJSPlugin(jsFile) {
		t.Errorf("isJSPlugin(%q) = false, want true", jsFile)
	}
}

func TestIsJSPluginUnknown(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"nonexistent", "/nonexistent/path/file.txt"},
		{"directory", t.TempDir()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isJSPlugin(tt.path) {
				t.Errorf("isJSPlugin(%q) = true, want false", tt.path)
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
			got := ExpandPath(tt.input)
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoadAllNonJSPlugin(t *testing.T) {
	tmp := t.TempDir()
	goDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(discardLogger(), nil)
	// Non-.js paths should be skipped without error.
	err := m.LoadAll([]config.PluginConfig{
		{Path: goDir, Config: nil},
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(m.plugins) != 0 {
		t.Errorf("expected 0 plugins loaded, got %d", len(m.plugins))
	}
}

func TestCloseReturnsNilForNilRuntimes(t *testing.T) {
	m := NewManager(discardLogger(), nil)

	m.plugins = append(m.plugins,
		&JSPlugin{name: "ok"},
		&JSPlugin{name: "also-ok"},
	)

	err := m.Close()
	if err != nil {
		t.Fatalf("expected nil error from Close with nil runtimes, got: %v", err)
	}
}
