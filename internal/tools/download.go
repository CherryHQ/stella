package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/vaayne/anna/pkg/httpclient"
)

const githubBaseURL = "https://github.com"

var httpClient = httpclient.New()

// manifestMu serializes manifest read-modify-write operations across concurrent downloads.
var manifestMu sync.Mutex

// ToolStatus reports the install state of a tool.
type ToolStatus struct {
	Name      string
	Version   string
	Installed bool
	Current   bool // installed version matches registry version
}

// Download fetches and installs a single tool to binDir for the given platform.
// If the tool is already installed at the correct version, it is a no-op.
// Uses the tool's default version from the registry.
func Download(ctx context.Context, tool *Tool, binDir, platform string) error {
	return DownloadVersion(ctx, tool, tool.Version, binDir, platform)
}

// DownloadLatest fetches the latest release from GitHub and installs it.
func DownloadLatest(ctx context.Context, tool *Tool, binDir, platform string) error {
	latest, err := FetchLatestVersion(ctx, tool)
	if err != nil {
		slog.Warn("failed to fetch latest version, using default", "tool", tool.Name, "error", err)
		return DownloadVersion(ctx, tool, tool.Version, binDir, platform)
	}
	return DownloadVersion(ctx, tool, latest, binDir, platform)
}

// DownloadVersion fetches and installs a specific version of a tool.
// The manifest check and save are serialized via manifestMu so concurrent
// goroutines cannot clobber each other's entries.
func DownloadVersion(ctx context.Context, tool *Tool, version, binDir, platform string) error {
	manifestMu.Lock()
	manifest, err := LoadManifest(binDir)
	if err != nil {
		manifestMu.Unlock()
		return err
	}
	if manifest.IsInstalled(tool.Name, version) {
		manifestMu.Unlock()
		slog.Info("tool already installed", "name", tool.Name, "version", version)
		return nil
	}
	manifestMu.Unlock()

	asset, ok := tool.ResolveAsset(platform, version)
	if !ok {
		return fmt.Errorf("no asset for %s on platform %s", tool.Name, platform)
	}

	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", githubBaseURL, tool.Repo, asset.Tag, asset.File)
	slog.Info("downloading tool", "name", tool.Name, "version", version, "url", url)

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "anna-tool-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	dlPath := filepath.Join(tmpDir, asset.File)
	if err := downloadToFile(ctx, url, dlPath); err != nil {
		return err
	}

	binaryName := tool.Name
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}

	var extractedPath string
	if asset.RawBinary {
		extractedPath = dlPath
	} else {
		extractedPath, err = extractBinaryFromArchive(dlPath, tmpDir, binaryName)
		if err != nil {
			return err
		}
	}

	targetPath := filepath.Join(binDir, binaryName)
	if err := atomicInstall(extractedPath, targetPath); err != nil {
		return err
	}

	// Reload manifest under lock to capture any concurrent writes before saving.
	manifestMu.Lock()
	manifest, err = LoadManifest(binDir)
	if err != nil {
		manifestMu.Unlock()
		return err
	}
	manifest.Tools[tool.Name] = InstalledTool{Version: version, Platform: platform}
	err = manifest.Save(binDir)
	manifestMu.Unlock()
	return err
}

func downloadToFile(ctx context.Context, url, dest string) error {
	resp, err := httpClient.R().
		SetContext(ctx).
		SetOutput(dest).
		Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode())
	}
	return nil
}

func extractBinaryFromArchive(archivePath, destDir, binaryName string) (string, error) {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"):
		return extractBinaryFromTarGz(archivePath, destDir, binaryName)
	case strings.HasSuffix(archivePath, ".zip"):
		return extractBinaryFromZip(archivePath, destDir, binaryName)
	default:
		return "", fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
}

func extractBinaryFromTarGz(archivePath, destDir, binaryName string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(header.Name) != binaryName {
			continue
		}
		outPath := filepath.Join(destDir, binaryName)
		if err := writeReaderToFile(outPath, tarReader, 0o755); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractBinaryFromZip(archivePath, destDir, binaryName string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open zip archive: %w", err)
	}
	defer func() { _ = reader.Close() }()

	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open zip entry: %w", err)
		}
		outPath := filepath.Join(destDir, binaryName)
		writeErr := writeReaderToFile(outPath, rc, 0o755)
		closeErr := rc.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", fmt.Errorf("close zip entry: %w", closeErr)
		}
		return outPath, nil
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func writeReaderToFile(path string, reader io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		return fmt.Errorf("write file: %w", err)
	}
	return out.Close()
}

func atomicInstall(srcPath, targetPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source binary: %w", err)
	}

	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)
	out, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		_ = in.Close()
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := out.Name()

	_, copyErr := io.Copy(out, in)
	_ = in.Close()
	_ = out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", copyErr)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}
