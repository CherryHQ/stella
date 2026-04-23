package builddeps

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	fdVersion            = "10.4.2"
	fdDarwinAMD64Version = "10.3.0"
	rgVersion            = "15.1.0"
	boxshVersion         = "2.1.0"
)

type embeddedBinaryAsset struct {
	File       string
	Tag        string
	RawBinary  bool
	BinaryName string
}

type embeddedBinary struct {
	Name           string
	Repo           string
	Version        string
	Tag            string
	Optional       bool
	AssetTemplates map[string]embeddedBinaryAsset
}

func (b embeddedBinary) resolveAsset(platform string) (embeddedBinaryAsset, string, bool) {
	asset, ok := b.AssetTemplates[platform]
	if !ok {
		return embeddedBinaryAsset{}, "", false
	}
	tag := asset.Tag
	if tag == "" {
		tag = b.Tag
	}
	if tag == "" {
		tag = ensureVPrefix(b.Version)
	}
	asset.File = strings.ReplaceAll(asset.File, "{version}", b.Version)
	asset.File = strings.ReplaceAll(asset.File, "{tag}", tag)
	if asset.BinaryName == "" {
		asset.BinaryName = b.Name
	}
	return asset, tag, true
}

func embeddedBinaries() []embeddedBinary {
	return []embeddedBinary{
		{
			Name:    "fd",
			Repo:    "sharkdp/fd",
			Version: fdVersion,
			AssetTemplates: map[string]embeddedBinaryAsset{
				"darwin-amd64":  {File: "fd-v" + fdDarwinAMD64Version + "-x86_64-apple-darwin.tar.gz", Tag: "v" + fdDarwinAMD64Version},
				"darwin-arm64":  {File: "fd-v{version}-aarch64-apple-darwin.tar.gz"},
				"linux-amd64":   {File: "fd-v{version}-x86_64-unknown-linux-musl.tar.gz"},
				"linux-arm64":   {File: "fd-v{version}-aarch64-unknown-linux-musl.tar.gz"},
				"windows-amd64": {File: "fd-v{version}-x86_64-pc-windows-msvc.zip", BinaryName: "fd.exe"},
				"windows-arm64": {File: "fd-v{version}-aarch64-pc-windows-msvc.zip", BinaryName: "fd.exe"},
			},
		},
		{
			Name:    "rg",
			Repo:    "BurntSushi/ripgrep",
			Version: rgVersion,
			Tag:     rgVersion,
			AssetTemplates: map[string]embeddedBinaryAsset{
				"darwin-amd64":  {File: "ripgrep-{version}-x86_64-apple-darwin.tar.gz"},
				"darwin-arm64":  {File: "ripgrep-{version}-aarch64-apple-darwin.tar.gz"},
				"linux-amd64":   {File: "ripgrep-{version}-x86_64-unknown-linux-musl.tar.gz"},
				"linux-arm64":   {File: "ripgrep-{version}-aarch64-unknown-linux-gnu.tar.gz"},
				"windows-amd64": {File: "ripgrep-{version}-x86_64-pc-windows-msvc.zip", BinaryName: "rg.exe"},
				"windows-arm64": {File: "ripgrep-{version}-aarch64-pc-windows-msvc.zip", BinaryName: "rg.exe"},
			},
		},
		{
			Name:     "boxsh",
			Repo:     "xicilion/boxsh",
			Version:  boxshVersion,
			Optional: true,
			AssetTemplates: map[string]embeddedBinaryAsset{
				"darwin-amd64": {File: "boxsh-v{version}-darwin-x86_64", RawBinary: true},
				"darwin-arm64": {File: "boxsh-v{version}-darwin-arm64", RawBinary: true},
				"linux-amd64":  {File: "boxsh-v{version}-linux-x64", RawBinary: true},
				"linux-arm64":  {File: "boxsh-v{version}-linux-arm64", RawBinary: true},
			},
		},
	}
}

type toolSyncer struct {
	client  *http.Client
	baseURL string
	specs   []embeddedBinary
}

// SyncEmbeddedTools downloads and stores the embedded third-party binaries for
// the target platform. Downloads are integrity-checked only by HTTPS + GitHub's
// own release infrastructure; no additional SHA256 manifest is verified. This
// is acceptable for a dev-time build tool but should be revisited if this path
// ever runs with write access to a production artifact store.
func SyncEmbeddedTools(ctx context.Context, cfg Config) error {
	specs, err := embeddedBinaryCatalog()
	if err != nil {
		return err
	}
	s := toolSyncer{
		client:  http.DefaultClient,
		baseURL: "https://github.com",
		specs:   specs,
	}
	return s.sync(ctx, cfg)
}

func embeddedBinaryCatalog() ([]embeddedBinary, error) {
	return embeddedBinaries(), nil
}

func (s toolSyncer) sync(ctx context.Context, cfg Config) error {
	platform := cfg.Platform()
	platformDir := filepath.Join(cfg.WorkDir, "internal", "resources", "binaries", "binaries", platform)
	for _, spec := range s.specs {
		binaryPath, cleanup, err := s.fetchBinary(ctx, spec, platform)
		if err != nil {
			if spec.Optional && strings.Contains(err.Error(), "no asset for") {
				continue
			}
			return fmt.Errorf("sync %s: %w", spec.Name, err)
		}
		targetPath := filepath.Join(platformDir, spec.Name+".gz")
		gzipErr := gzipFile(binaryPath, targetPath)
		cleanup()
		if gzipErr != nil {
			return fmt.Errorf("sync %s: %w", spec.Name, gzipErr)
		}
	}
	return nil
}

func (s toolSyncer) fetchBinary(ctx context.Context, spec embeddedBinary, platform string) (string, func(), error) {
	asset, tag, ok := spec.resolveAsset(platform)
	if !ok {
		return "", func() {}, fmt.Errorf("no asset for %s on platform %s", spec.Name, platform)
	}
	url := GitHubReleaseAssetURL(spec.Repo, tag, asset.File)
	if s.baseURL != "https://github.com" {
		url = fmt.Sprintf("%s/%s/releases/download/%s/%s", s.baseURL, spec.Repo, tag, asset.File)
	}
	return s.fetchAssetBinary(ctx, asset, url)
}

func (s toolSyncer) fetchAssetBinary(ctx context.Context, asset embeddedBinaryAsset, url string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "anna-builddeps-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	archivePath := filepath.Join(tmpDir, filepath.Base(asset.File))
	if err := Download(ctx, s.client, url, archivePath); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if asset.RawBinary {
		return archivePath, cleanup, nil
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("create extract dir: %w", err)
	}
	if err := extractArchive(archivePath, extractDir); err != nil {
		cleanup()
		return "", func() {}, err
	}
	binaryPath, err := findExtractedBinary(extractDir, asset.BinaryName)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return binaryPath, cleanup, nil
}

func extractArchive(archivePath, destDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"):
		return ExtractTarGz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		return ExtractZip(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
}

func findExtractedBinary(root, binaryName string) (string, error) {
	var match string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) == binaryName {
			match = path
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("scan extracted archive: %w", err)
	}
	if match == "" {
		return "", fmt.Errorf("binary %q not found in extracted archive", binaryName)
	}
	return match, nil
}

func gzipFile(srcPath, destPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", destPath, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".gzip-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", destPath, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	gz, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("create gzip writer: %w", err)
	}
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = tmp.Close()
		return fmt.Errorf("gzip %s: %w", srcPath, err)
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("close gzip writer for %s: %w", destPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", destPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename %s: %w", destPath, err)
	}
	return nil
}

func ensureVPrefix(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
