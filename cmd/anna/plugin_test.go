package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestYesNo(t *testing.T) {
	if got := yesNo(true); got != "yes" {
		t.Errorf("yesNo(true) = %q, want yes", got)
	}
	if got := yesNo(false); got != "no" {
		t.Errorf("yesNo(false) = %q, want no", got)
	}
}

func TestTruncateJSON(t *testing.T) {
	tests := []struct {
		name   string
		m      map[string]any
		maxLen int
		want   string
	}{
		{"empty", map[string]any{}, 40, "{}"},
		{"nil", nil, 40, "{}"},
		{"short", map[string]any{"k": "v"}, 40, `{"k":"v"}`},
		{"long", map[string]any{"very_long_key": "very_long_value_here"}, 20, `{"very_long_key":...`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateJSON(tt.m, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsBuiltinPlugin(t *testing.T) {
	if !isBuiltinPlugin("tool/read") {
		t.Error("expected tool/read to be builtin")
	}
	if !isBuiltinPlugin("channel/telegram") {
		t.Error("expected channel/telegram to be builtin")
	}
	if isBuiltinPlugin("tool/custom") {
		t.Error("expected tool/custom to not be builtin")
	}
}

func TestParseConfigValue(t *testing.T) {
	tests := []struct {
		input string
		want  any
	}{
		{"42", float64(42)},
		{"true", true},
		{"false", false},
		{`"hello"`, "hello"},
		{"hello", "hello"},
		{`{"k":"v"}`, map[string]any{"k": "v"}},
	}
	for _, tt := range tests {
		got := parseConfigValue(tt.input)
		switch v := tt.want.(type) {
		case float64:
			if g, ok := got.(float64); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case bool:
			if g, ok := got.(bool); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case string:
			if g, ok := got.(string); !ok || g != v {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		case map[string]any:
			if g, ok := got.(map[string]any); !ok || g["k"] != v["k"] {
				t.Errorf("parseConfigValue(%q) = %v, want %v", tt.input, got, v)
			}
		}
	}
}

func TestLoadPluginManifest(t *testing.T) {
	dir := t.TempDir()

	// No plugin.json → error
	_, err := loadPluginManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing plugin.json")
	}

	// Valid plugin.json
	manifest := `{"name":"test","kind":"tool","version":"1.0.0"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := loadPluginManifest(dir)
	if err != nil {
		t.Fatalf("loadPluginManifest: %v", err)
	}
	if m["name"] != "test" || m["kind"] != "tool" {
		t.Errorf("unexpected manifest: %v", m)
	}

	// Invalid JSON
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = loadPluginManifest(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")

	// Create source structure
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Executable file
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// Verify files copied
	data, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil || string(data) != "hello" {
		t.Errorf("a.txt not copied correctly")
	}
	data, err = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil || string(data) != "world" {
		t.Errorf("sub/b.txt not copied correctly")
	}

	// Verify executable permission preserved
	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("run.sh lost execute permission")
	}
}
