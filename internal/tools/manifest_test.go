package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest_Missing(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Tools == nil {
		t.Fatal("expected non-nil Tools map")
	}
	if len(m.Tools) != 0 {
		t.Fatalf("expected empty map, got %v", m.Tools)
	}
}

func TestLoadManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	data := `{"tools":{"fd":{"version":"8.7.0","platform":"linux/amd64"}}}`
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tool, ok := m.Tools["fd"]
	if !ok {
		t.Fatal("expected fd in tools")
	}
	if tool.Version != "8.7.0" {
		t.Fatalf("unexpected version: %s", tool.Version)
	}
}

func TestLoadManifest_Corrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("expected error for corrupt manifest")
	}
}

func TestManifestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Tools: map[string]InstalledTool{
		"rg": {Version: "13.0.0", Platform: "darwin/arm64"},
	}}
	if err := m.Save(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var loaded Manifest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Tools["rg"].Version != "13.0.0" {
		t.Fatalf("unexpected version after save: %v", loaded.Tools)
	}
}

func TestToolResolveAsset(t *testing.T) {
	tool := &Tool{
		Name:    "fd",
		Version: "8.7.0",
		AssetTemplates: map[string]AssetTemplate{
			"linux-amd64": {File: "fd-{version}-x86_64-linux.tar.gz"},
			"raw":         {File: "mybin-{tag}", RawBinary: true},
		},
	}

	t.Run("known platform", func(t *testing.T) {
		a, ok := tool.ResolveAsset("linux-amd64", "8.7.0")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if a.File != "fd-8.7.0-x86_64-linux.tar.gz" {
			t.Fatalf("unexpected file: %s", a.File)
		}
		if a.Tag != "v8.7.0" {
			t.Fatalf("unexpected tag: %s", a.Tag)
		}
	})
	t.Run("unknown platform", func(t *testing.T) {
		_, ok := tool.ResolveAsset("plan9-386", "8.7.0")
		if ok {
			t.Fatal("expected ok=false for unknown platform")
		}
	})
	t.Run("tag substitution", func(t *testing.T) {
		a, ok := tool.ResolveAsset("raw", "1.2.3")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if a.File != "mybin-v1.2.3" {
			t.Fatalf("unexpected file: %s", a.File)
		}
		if !a.RawBinary {
			t.Fatal("expected RawBinary=true")
		}
	})
	t.Run("version already has v prefix", func(t *testing.T) {
		tool2 := &Tool{
			AssetTemplates: map[string]AssetTemplate{
				"linux-amd64": {File: "fd-{tag}.tar.gz"},
			},
		}
		a, _ := tool2.ResolveAsset("linux-amd64", "v9.0.0")
		if a.Tag != "v9.0.0" {
			t.Fatalf("unexpected tag: %s", a.Tag)
		}
	})
}

func TestBinDir(t *testing.T) {
	got := BinDir("/home/user/.anna")
	if got != "/home/user/.anna/bin" {
		t.Fatalf("unexpected BinDir: %s", got)
	}
}

func TestManifestIsInstalled(t *testing.T) {
	m := &Manifest{Tools: map[string]InstalledTool{
		"fd": {Version: "8.7.0"},
	}}
	if !m.IsInstalled("fd", "8.7.0") {
		t.Fatal("expected IsInstalled=true")
	}
	if m.IsInstalled("fd", "9.0.0") {
		t.Fatal("expected IsInstalled=false for wrong version")
	}
	if m.IsInstalled("rg", "8.7.0") {
		t.Fatal("expected IsInstalled=false for unknown tool")
	}
}
