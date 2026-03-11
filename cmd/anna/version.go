package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
)

const (
	githubOwner = "vaayne"
	githubRepo  = "anna"
)

var (
	version             = "dev"
	upgradeHTTPClient   = &http.Client{Timeout: 30 * time.Second}
	upgradeAPIBaseURL   = "https://api.github.com"
	upgradeUserAgent    = "anna-upgrade"
	errUnsupportedAsset = errors.New("no release asset for current platform")
	errInvalidTarget    = errors.New("existing target is not a replaceable file")
	renameFile          = os.Rename
)

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type upgradeResult struct {
	CurrentVersion string
	LatestVersion  string
	TargetPath     string
	AlreadyCurrent bool
}

func versionCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "version",
		Usage: "Show the current anna version",
		Action: func(c *ucli.Context) error {
			fmt.Println(displayVersion())
			return nil
		},
	}
}

func upgradeCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "upgrade",
		Usage: "Upgrade anna to the latest stable GitHub release",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "install-dir",
				Usage: "Directory to install the upgraded binary into",
			},
		},
		Action: func(c *ucli.Context) error {
			installDir := c.String("install-dir")
			if installDir == "" {
				dir, err := defaultInstallDir()
				if err != nil {
					return err
				}
				installDir = dir
			}

			result, err := runUpgrade(c.Context, installDir, displayVersion(), runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			if result.AlreadyCurrent {
				fmt.Printf("anna %s is already up to date\n", result.CurrentVersion)
				return nil
			}

			fmt.Printf("Upgraded anna from %s to %s\n", result.CurrentVersion, result.LatestVersion)
			fmt.Printf("Installed to %s\n", result.TargetPath)
			return nil
		},
	}
}

func displayVersion() string {
	normalized := normalizeVersion(version)
	if normalized == "" {
		return "dev"
	}
	return normalized
}

func defaultInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func fetchLatestRelease(ctx context.Context) (release *githubRelease, err error) {
	url := strings.TrimRight(upgradeAPIBaseURL, "/") + "/repos/" + githubOwner + "/" + githubRepo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", upgradeUserAgent)

	resp, err := upgradeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close release response body: %w", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("fetch latest release: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode release response: %w", err)
	}
	if normalizeVersion(parsed.TagName) == "" {
		return nil, fmt.Errorf("latest release tag %q is invalid", parsed.TagName)
	}
	return &parsed, nil
}

func runUpgrade(ctx context.Context, installDir, currentVersion, goos, goarch string) (*upgradeResult, error) {
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return nil, err
	}

	result := &upgradeResult{
		CurrentVersion: normalizeVersion(currentVersion),
		LatestVersion:  normalizeVersion(release.TagName),
	}
	if result.CurrentVersion == "" {
		result.CurrentVersion = "dev"
	}
	if result.LatestVersion == "" {
		return nil, fmt.Errorf("latest release tag %q is invalid", release.TagName)
	}
	if result.LatestVersion == normalizeVersion(currentVersion) {
		result.AlreadyCurrent = true
		return result, nil
	}

	asset, err := selectReleaseAsset(release.Assets, goos, goarch)
	if err != nil {
		return nil, err
	}

	targetPath, err := installReleaseAsset(ctx, asset, installDir, goos)
	if err != nil {
		return nil, err
	}
	result.TargetPath = targetPath
	return result, nil
}

func selectReleaseAsset(assets []githubReleaseAsset, goos, goarch string) (githubReleaseAsset, error) {
	suffixes := releaseAssetSuffixes(goos, goarch)
	for _, asset := range assets {
		for _, suffix := range suffixes {
			if strings.HasSuffix(asset.Name, suffix) {
				return asset, nil
			}
		}
	}
	return githubReleaseAsset{}, fmt.Errorf("%w: %s/%s", errUnsupportedAsset, goos, goarch)
}

func releaseAssetSuffixes(goos, goarch string) []string {
	base := "_" + goos + "_" + goarch
	return []string{base + ".tar.gz", base + ".zip"}
}

func installReleaseAsset(ctx context.Context, asset githubReleaseAsset, installDir, goos string) (targetPath string, err error) {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", fmt.Errorf("create install dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "anna-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil && err == nil {
			err = fmt.Errorf("cleanup temp dir: %w", removeErr)
		}
	}()

	archivePath := filepath.Join(tmpDir, asset.Name)
	if err := downloadFile(ctx, asset.BrowserDownloadURL, archivePath); err != nil {
		return "", err
	}

	binaryName := binaryNameForGOOS(goos)
	extractedPath, err := extractBinaryFromArchive(archivePath, tmpDir, binaryName)
	if err != nil {
		return "", err
	}

	targetPath = filepath.Join(installDir, binaryName)
	if err := installBinary(extractedPath, targetPath, goos != "windows"); err != nil {
		return "", err
	}
	return targetPath, nil
}

func downloadFile(ctx context.Context, url, dest string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build asset request: %w", err)
	}
	req.Header.Set("User-Agent", upgradeUserAgent)

	resp, err := upgradeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close asset response body: %w", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("download asset: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return fmt.Errorf("write archive file: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
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

func extractBinaryFromTarGz(archivePath, destDir, binaryName string) (outPath string, err error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close archive: %w", closeErr)
		}
	}()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() {
		if closeErr := gzReader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close gzip archive: %w", closeErr)
		}
	}()

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
		outPath = filepath.Join(destDir, binaryName)
		if err := writeReaderToFile(outPath, tarReader, 0o755); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractBinaryFromZip(archivePath, destDir, binaryName string) (outPath string, err error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open zip archive: %w", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close zip archive: %w", closeErr)
		}
	}()

	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open zip entry: %w", err)
		}
		outPath = filepath.Join(destDir, binaryName)
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
		return fmt.Errorf("create extracted binary: %w", err)
	}

	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		return fmt.Errorf("write extracted binary: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close extracted binary: %w", err)
	}
	return nil
}

func installBinary(srcPath, targetPath string, executable bool) error {
	tmpPath := targetPath + ".tmp"
	backupPath := targetPath + ".bak"
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open extracted binary: %w", err)
	}

	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create temp install file: %w", err)
	}

	_, copyErr := io.Copy(out, in)
	inputCloseErr := in.Close()
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("write temp install file: %w", copyErr)
	}
	if inputCloseErr != nil {
		return fmt.Errorf("close extracted binary: %w", inputCloseErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temp install file: %w", closeErr)
	}

	if executable {
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			return fmt.Errorf("chmod temp install file: %w", err)
		}
	}

	if err := ensureReplaceableTarget(targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if _, err := os.Lstat(targetPath); err == nil {
		if err := renameFile(targetPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("move existing binary aside: %w", err)
		}
		defer func() {
			if _, err := os.Stat(backupPath); err == nil {
				_ = os.RemoveAll(backupPath)
			}
		}()
	}

	if err := renameFile(tmpPath, targetPath); err != nil {
		if _, statErr := os.Stat(backupPath); statErr == nil {
			if restoreErr := renameFile(backupPath, targetPath); restoreErr != nil {
				return fmt.Errorf("install binary: %w (restore failed: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("install binary: %w", err)
	}

	if _, err := os.Stat(backupPath); err == nil {
		if removeErr := os.RemoveAll(backupPath); removeErr != nil {
			return fmt.Errorf("cleanup previous binary backup: %w", removeErr)
		}
	}
	return nil
}

func ensureReplaceableTarget(targetPath string) error {
	info, err := os.Lstat(targetPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat existing target: %w", err)
	}

	mode := info.Mode()
	if mode.IsRegular() || mode&os.ModeSymlink != 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errInvalidTarget, targetPath)
}

func binaryNameForGOOS(goos string) string {
	if goos == "windows" {
		return "anna.exe"
	}
	return "anna"
}

func normalizeVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, ".")
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return trimmed
			}
		}
	}
	return trimmed
}
