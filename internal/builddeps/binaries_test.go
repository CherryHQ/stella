package builddeps

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestEmbeddedBinariesIncludeGitHubCLI(t *testing.T) {
	found, err := embeddedBinaryFromPluginSpec(pkgplugins.BinarySpec{
		PluginID: "tool/gh",
		Name:     "gh",
		Repo:     "cli/cli",
		Version:  "2.89.0",
		AssetTemplates: map[string]pkgplugins.BinaryAsset{
			"darwin-amd64":  {File: "gh_{version}_macOS_amd64.zip"},
			"darwin-arm64":  {File: "gh_{version}_macOS_arm64.zip"},
			"linux-amd64":   {File: "gh_{version}_linux_amd64.tar.gz"},
			"linux-arm64":   {File: "gh_{version}_linux_arm64.tar.gz"},
			"windows-amd64": {File: "gh_{version}_windows_amd64.zip"},
			"windows-arm64": {File: "gh_{version}_windows_arm64.zip"},
		},
	})
	if err != nil {
		t.Fatalf("embeddedBinaryFromPluginSpec: %v", err)
	}
	if found.Version != "2.89.0" {
		t.Fatalf("version = %q, want %q", found.Version, "2.89.0")
	}
	for _, platform := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"} {
		if _, _, ok := found.resolveAsset(platform); !ok {
			t.Fatalf("missing gh asset for %s", platform)
		}
	}
}

func TestEmbeddedBinariesFDDarwinAMD64UsesLegacyTag(t *testing.T) {
	specs := embeddedBinaries()
	var found *embeddedBinary
	for i := range specs {
		if specs[i].Name == "fd" {
			found = &specs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("fd spec not found")
	}
	asset, tag, ok := found.resolveAsset("darwin-amd64")
	if !ok {
		t.Fatal("missing fd darwin-amd64 asset")
	}
	if tag != "v"+fdDarwinAMD64Version {
		t.Fatalf("tag = %q, want %q", tag, "v"+fdDarwinAMD64Version)
	}
	if asset.File != "fd-v10.3.0-x86_64-apple-darwin.tar.gz" {
		t.Fatalf("file = %q", asset.File)
	}
}

func TestEmbeddedBinariesIncludeLarkCLI(t *testing.T) {
	found, err := embeddedBinaryFromPluginSpec(pkgplugins.BinarySpec{
		PluginID: "tool/lark-cli",
		Name:     "lark-cli",
		Repo:     "larksuite/cli",
		Version:  "1.0.15",
		AssetTemplates: map[string]pkgplugins.BinaryAsset{
			"darwin-amd64":  {File: "lark-cli-{version}-darwin-amd64.tar.gz"},
			"darwin-arm64":  {File: "lark-cli-{version}-darwin-arm64.tar.gz"},
			"linux-amd64":   {File: "lark-cli-{version}-linux-amd64.tar.gz"},
			"linux-arm64":   {File: "lark-cli-{version}-linux-arm64.tar.gz"},
			"windows-amd64": {File: "lark-cli-{version}-windows-amd64.zip"},
			"windows-arm64": {File: "lark-cli-{version}-windows-arm64.zip"},
		},
	})
	if err != nil {
		t.Fatalf("embeddedBinaryFromPluginSpec: %v", err)
	}
	if found.Version != "1.0.15" {
		t.Fatalf("version = %q, want %q", found.Version, "1.0.15")
	}
	for _, platform := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"} {
		if _, _, ok := found.resolveAsset(platform); !ok {
			t.Fatalf("missing lark-cli asset for %s", platform)
		}
	}
}

func TestToolSyncerSyncWritesTargetGzip(t *testing.T) {
	archiveData := makeTarGz(t, map[string]string{"ripgrep-15.1.0-x86_64-apple-darwin/rg": "rg-binary"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ripgrep-15.1.0-x86_64-apple-darwin.tar.gz") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	root := t.TempDir()
	s := toolSyncer{
		client:  server.Client(),
		baseURL: server.URL,
		specs: []embeddedBinary{{
			Name:    "rg",
			Repo:    "BurntSushi/ripgrep",
			Version: "15.1.0",
			AssetTemplates: map[string]embeddedBinaryAsset{
				"darwin-amd64": {File: "ripgrep-{version}-x86_64-apple-darwin.tar.gz", BinaryName: "rg"},
			},
		}},
	}
	if err := s.sync(context.Background(), Config{WorkDir: root, SyncTools: true, GOOS: "darwin", GOARCH: "amd64"}.Normalized()); err != nil {
		t.Fatalf("sync() error = %v", err)
	}
	target := filepath.Join(root, "internal", "resources", "binaries", "binaries", "darwin-amd64", "rg.gz")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gr.Close() }()
	plain, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "rg-binary" {
		t.Fatalf("gzip payload = %q, want rg-binary", string(plain))
	}
}

func TestToolSyncerSyncWritesWindowsZipTargetGzip(t *testing.T) {
	zipData := makeZip(t, map[string]string{"ripgrep-15.1.0-x86_64-pc-windows-msvc/rg.exe": "rg-windows"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ripgrep-15.1.0-x86_64-pc-windows-msvc.zip") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write(zipData)
	}))
	defer server.Close()

	root := t.TempDir()
	s := toolSyncer{
		client:  server.Client(),
		baseURL: server.URL,
		specs: []embeddedBinary{{
			Name:    "rg",
			Repo:    "BurntSushi/ripgrep",
			Version: "15.1.0",
			AssetTemplates: map[string]embeddedBinaryAsset{
				"windows-amd64": {File: "ripgrep-{version}-x86_64-pc-windows-msvc.zip", BinaryName: "rg.exe"},
			},
		}},
	}
	if err := s.sync(context.Background(), Config{WorkDir: root, SyncTools: true, GOOS: "windows", GOARCH: "amd64"}.Normalized()); err != nil {
		t.Fatalf("sync() error = %v", err)
	}
	target := filepath.Join(root, "internal", "resources", "binaries", "binaries", "windows-amd64", "rg.gz")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gr.Close() }()
	plain, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "rg-windows" {
		t.Fatalf("gzip payload = %q, want rg-windows", string(plain))
	}
}

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
