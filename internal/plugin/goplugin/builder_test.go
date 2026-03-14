package goplugin

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestNewBuilderDefaults(t *testing.T) {
	b := NewBuilder([]string{"/some/plugin"}, "", nil)

	if b.output != defaultOutput {
		t.Errorf("expected default output %q, got %q", defaultOutput, b.output)
	}
	if b.logger == nil {
		t.Error("expected non-nil logger when nil is passed")
	}
	if len(b.plugins) != 1 || b.plugins[0] != "/some/plugin" {
		t.Errorf("expected plugins [/some/plugin], got %v", b.plugins)
	}
}

func TestNewBuilderCustomOutput(t *testing.T) {
	b := NewBuilder([]string{}, "/tmp/my-binary", slog.Default())

	if b.output != "/tmp/my-binary" {
		t.Errorf("expected output %q, got %q", "/tmp/my-binary", b.output)
	}
}

func TestReadModulePath(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMod string
		wantErr bool
	}{
		{
			name:    "standard module line",
			content: "module example.com/my-plugin\n\ngo 1.25\n",
			wantMod: "example.com/my-plugin",
		},
		{
			name:    "module line with extra whitespace",
			content: "  module   github.com/foo/bar  \n\ngo 1.25\n",
			wantMod: "github.com/foo/bar",
		},
		{
			name:    "module line after comments",
			content: "// this is a comment\nmodule example.com/test\n",
			wantMod: "example.com/test",
		},
		{
			name:    "no module directive",
			content: "go 1.25\n\nrequire example.com/dep v1.0.0\n",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gomodPath := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(gomodPath, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write temp go.mod: %v", err)
			}

			got, err := readModulePath(gomodPath)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got module %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantMod {
				t.Errorf("expected module %q, got %q", tc.wantMod, got)
			}
		})
	}
}

func TestReadModulePathNotFound(t *testing.T) {
	_, err := readModulePath("/nonexistent/path/go.mod")
	if err == nil {
		t.Error("expected error for missing go.mod, got nil")
	}
}

func TestMainGoTemplate(t *testing.T) {
	data := templateData{
		AnnaModule:    "github.com/vaayne/anna",
		AnnaLocalPath: "/home/user/anna",
		Plugins: []pluginModule{
			{Module: "example.com/plugin-a", LocalPath: "/home/user/plugin-a"},
			{Module: "example.com/plugin-b", LocalPath: "/home/user/plugin-b"},
		},
	}

	var buf bytes.Buffer
	if err := mainGoTmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	out := buf.String()

	checks := []string{
		"package main",
		`_ "example.com/plugin-a"`,
		`_ "example.com/plugin-b"`,
		`anna "github.com/vaayne/anna/cmd/anna"`,
		"anna.Main()",
	}
	for _, want := range checks {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("main.go output missing %q\n\nfull output:\n%s", want, out)
		}
	}
}

func TestGoModTemplate(t *testing.T) {
	data := templateData{
		AnnaModule:    "github.com/vaayne/anna",
		AnnaLocalPath: "/home/user/anna",
		Plugins: []pluginModule{
			{Module: "example.com/plugin-a", LocalPath: "/home/user/plugin-a"},
		},
	}

	var buf bytes.Buffer
	if err := goModTmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	out := buf.String()

	checks := []string{
		"module anna-custom",
		"go 1.25",
		"github.com/vaayne/anna v0.0.0",
		"example.com/plugin-a v0.0.0",
		"replace github.com/vaayne/anna => /home/user/anna",
		"replace example.com/plugin-a => /home/user/plugin-a",
	}
	for _, want := range checks {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("go.mod output missing %q\n\nfull output:\n%s", want, out)
		}
	}
}

func TestBuildMissingPlugin(t *testing.T) {
	b := NewBuilder(
		[]string{"/nonexistent/plugin/path"},
		filepath.Join(t.TempDir(), "anna-test"),
		slog.Default(),
	)

	err := b.Build(context.Background())
	if err == nil {
		t.Fatal("expected error for non-existent plugin path, got nil")
	}
}
