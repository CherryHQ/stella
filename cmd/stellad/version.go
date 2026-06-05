package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/version"
	"github.com/CherryHQ/stella/pkg/httpclient"
)

const (
	githubOwner = "CherryHQ"
	githubRepo  = "stella"
)

var (
	upgradeHTTPClient   = httpclient.New()
	upgradeAPIBaseURL   = "https://api.github.com"
	upgradeUserAgent    = "stella-upgrade"
	errUnsupportedAsset = errors.New("no release asset for current platform")
	errInvalidTarget    = errors.New("existing target is not a replaceable file")
	executablePath      = os.Executable
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
	InstallDir     string
	AlreadyCurrent bool
}

func versionCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "version",
		Usage:    "Show the current stella version",
		Category: "System",
		Action: func(c *ucli.Context) error {
			fmt.Println(displayVersion())
			return nil
		},
	}
}

func upgradeCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "upgrade",
		Usage:    "Upgrade stella to the latest stable GitHub release",
		Category: "System",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "install-dir",
				Usage: "Directory to install the upgraded binary into (defaults to the running stella binary's directory)",
			},
		},
		Action: func(c *ucli.Context) error {
			installDir, err := resolveUpgradeDir(c.String("install-dir"))
			if err != nil {
				return err
			}

			result, err := runUpgrade(c.Context, installDir, displayVersion(), runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			if result.AlreadyCurrent {
				fmt.Printf("stella %s is already up to date\n", result.CurrentVersion)
				return nil
			}

			fmt.Printf("Upgraded stella from %s to %s\n", result.CurrentVersion, result.LatestVersion)
			fmt.Printf("Installed to %s\n", installDir)
			return nil
		},
	}
}

func displayVersion() string {
	normalized := normalizeVersion(version.Version)
	if normalized == "" {
		return "dev"
	}
	return normalized
}

// resolveUpgradeDir returns the directory to install upgraded binaries into.
// When installDir is empty, it uses the running executable's directory.
func resolveUpgradeDir(installDir string) (string, error) {
	if installDir != "" {
		return installDir, nil
	}
	exePath, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	if exePath == "" {
		return "", errors.New("resolve executable path: empty path")
	}
	resolved, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlink: %w", err)
	}
	return filepath.Dir(resolved), nil
}

// binariesToUpgrade returns the binary names that must be present in a release
// archive for a full upgrade (both CLI and daemon).
func binariesToUpgrade(goos string) []string {
	if goos == "windows" {
		return []string{"stella.exe", "stellad.exe"}
	}
	return []string{"stella", "stellad"}
}

func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	url := strings.TrimRight(upgradeAPIBaseURL, "/") + "/repos/" + githubOwner + "/" + githubRepo + "/releases/latest"

	var parsed githubRelease
	resp, err := upgradeHTTPClient.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("User-Agent", upgradeUserAgent).
		SetResult(&parsed).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("fetch latest release: unexpected status %d: %s", resp.StatusCode(), strings.TrimSpace(resp.String()))
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

	if err := installReleaseAsset(ctx, asset, installDir, goos); err != nil {
		return nil, err
	}
	result.InstallDir = installDir
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

func installReleaseAsset(ctx context.Context, asset githubReleaseAsset, installDir, goos string) (err error) {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "stella-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil && err == nil {
			err = fmt.Errorf("cleanup temp dir: %w", removeErr)
		}
	}()

	archivePath := filepath.Join(tmpDir, asset.Name)
	if err := downloadFile(ctx, asset.BrowserDownloadURL, archivePath); err != nil {
		return err
	}

	bins := binariesToUpgrade(goos)

	// Extract all binaries first — fail fast before modifying anything.
	extracted := make(map[string]string, len(bins))
	for _, binName := range bins {
		path, err := extractBinaryFromArchive(archivePath, tmpDir, binName)
		if err != nil {
			return fmt.Errorf("extract %s: %w", binName, err)
		}
		extracted[binName] = path
	}

	// Pre-check all targets are replaceable before installing any.
	for _, binName := range bins {
		targetPath := filepath.Join(installDir, binName)
		if err := ensureReplaceableTarget(targetPath); err != nil {
			return upgradeInstallError(err, targetPath, goos)
		}
	}

	// Stage new binaries as .tmp files alongside their targets.
	staged := make(map[string]string, len(bins))
	for _, binName := range bins {
		targetPath := filepath.Join(installDir, binName)
		tmpPath, err := stageBinary(extracted[binName], targetPath, goos != "windows")
		if err != nil {
			for _, t := range staged {
				_ = os.Remove(t)
			}
			return upgradeInstallError(err, targetPath, goos)
		}
		staged[binName] = tmpPath
	}

	// Two-phase commit: back up all existing targets, then rename all
	// staged files into place. If any step fails, restore backups.
	var backedUp []string
	for _, binName := range bins {
		targetPath := filepath.Join(installDir, binName)
		bakPath := targetPath + ".bak"
		if _, err := os.Lstat(targetPath); err == nil {
			if err := renameFile(targetPath, bakPath); err != nil {
				rollbackUpgrade(nil, backedUp)
				for _, t := range staged {
					_ = os.Remove(t)
				}
				return fmt.Errorf("back up %s: %w", binName, err)
			}
			backedUp = append(backedUp, targetPath)
		}
	}

	var committed []string
	for _, binName := range bins {
		targetPath := filepath.Join(installDir, binName)
		if err := renameFile(staged[binName], targetPath); err != nil {
			rollbackUpgrade(committed, backedUp)
			return upgradeInstallError(err, targetPath, goos)
		}
		committed = append(committed, targetPath)
	}

	for _, p := range backedUp {
		_ = os.Remove(p + ".bak")
	}
	return nil
}

// stageBinary copies src to a .tmp file next to targetPath, returning
// the temp path. The caller is responsible for renaming or cleaning it up.
func stageBinary(srcPath, targetPath string, executable bool) (string, error) {
	tmpPath := targetPath + ".tmp"
	in, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open extracted binary: %w", err)
	}
	defer func() { _ = in.Close() }()

	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return "", fmt.Errorf("create staged binary: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write staged binary: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close staged binary: %w", err)
	}
	if executable {
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("chmod staged binary: %w", err)
		}
	}
	return tmpPath, nil
}

// rollbackUpgrade removes newly committed binaries and restores their backups.
// On Windows os.Rename cannot overwrite, so the new file must be removed first.
func rollbackUpgrade(committed, backedUp []string) {
	for _, p := range committed {
		_ = os.Remove(p)
	}
	for _, p := range backedUp {
		if err := renameFile(p+".bak", p); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "WARNING: failed to restore %s from backup: %v\n", p, err)
		}
	}
}

func downloadFile(ctx context.Context, url, dest string) error {
	resp, err := upgradeHTTPClient.R().
		SetContext(ctx).
		SetHeader("User-Agent", upgradeUserAgent).
		SetOutput(dest).
		Get(url)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("download asset: unexpected status %d", resp.StatusCode())
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

func upgradeInstallError(err error, targetPath, goos string) error {
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "text file busy") || strings.Contains(lower, "file busy") || strings.Contains(lower, "being used by another process") || strings.Contains(lower, "sharing violation") || strings.Contains(lower, "resource busy") {
		if goos == "windows" {
			return fmt.Errorf("%w\n%s is locked by a running process; stop stella first, then run the upgrade from another shell or install into a different directory with --install-dir", err, targetPath)
		}
		return fmt.Errorf("%w\n%s is busy; stop any running stella process or service that is using this binary, then retry", err, targetPath)
	}
	if errors.Is(err, os.ErrPermission) || strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied") {
		return fmt.Errorf("%w\npermission denied replacing %s; rerun as a user that can write to %s, or choose another target with --install-dir", err, targetPath, filepath.Dir(targetPath))
	}
	return err
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

func normalizeVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if trimmed == "" {
		return ""
	}
	for part := range strings.SplitSeq(trimmed, ".") {
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
