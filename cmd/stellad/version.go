package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
	"golang.org/x/term"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	"github.com/CherryHQ/stella/pkg/httpclient"
)

const (
	githubOwner = "CherryHQ"
	githubRepo  = "stella"

	// maxArchiveBytes caps the total decompressed size of a release archive
	// to defend against decompression bombs or CDN compromise. Release archives
	// include stellad, which can exceed 100 MB by itself.
	maxArchiveBytes = 200 << 20 // 200 MB
)

var (
	upgradeHTTPClient = httpclient.New()
	// upgradeDownloadClient streams the release archive (100 MB+). It uses a
	// generous timeout so a slow link is not cut off mid-download; cancellation
	// still flows through the request context.
	upgradeDownloadClient = httpclient.NewWithTimeout(15 * time.Minute)
	upgradeAPIBaseURL     = "https://api.github.com"
	upgradeUserAgent      = "stella-upgrade"
	errUnsupportedAsset   = errors.New("no release asset for current platform")
	errInvalidTarget      = errors.New("existing target is not a replaceable file")
	executablePath        = os.Executable
	renameFile            = os.Rename
	removeFile            = os.Remove
	currentGOOS           = runtime.GOOS
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
			fmt.Println(version.DisplayVersion())
			return nil
		},
	}
}

func upgradeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "upgrade",
		Usage:     "Upgrade stella to a GitHub release (latest by default)",
		Category:  "System",
		ArgsUsage: "[version]",
		Description: "Upgrade stella to the latest stable GitHub release.\n" +
			"Pass an optional version (e.g. 0.50.0 or v0.50.0) to install that specific release instead.\n" +
			"RC builds require an explicit version or --channel stable to avoid an accidental downgrade.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "install-dir",
				Usage: "Directory to install the upgraded binary into (defaults to the running stella binary's directory)",
			},
			&ucli.StringFlag{
				Name:  "channel",
				Usage: "Release channel override (currently supported: stable)",
			},
		},
		Action: func(c *ucli.Context) error {
			if err := checkNativeServerPlatform(nativeServerGOOS); err != nil {
				return fmt.Errorf("upgrade: %w", err)
			}
			targetVersion := c.Args().First()
			channel := c.String("channel")
			if err := validateUpgradeRequest(version.DisplayVersion(), targetVersion, channel); err != nil {
				return err
			}
			installDir, err := resolveUpgradeDir(c.String("install-dir"))
			if err != nil {
				return err
			}

			result, err := runUpgrade(c.Context, os.Stdout, installDir, version.DisplayVersion(), targetVersion, nativeServerGOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			if result.AlreadyCurrent {
				fmt.Printf("stella %s is already up to date\n", result.CurrentVersion)
				return nil
			}

			fmt.Printf("\n%s Upgraded stella %s -> %s\n", okMark(os.Stdout), result.CurrentVersion, result.LatestVersion)
			fmt.Printf("  Installed to %s\n", installDir)
			syncPostgresRuntime(c.Context, os.Stdout, installDir, config.StellaHome(), nativeServerGOOS)
			return nil
		},
	}
}

func validateUpgradeRequest(currentVersion, targetVersion, channel string) error {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" && channel != "stable" {
		return fmt.Errorf("upgrade: unsupported channel %q (supported: stable)", channel)
	}
	if strings.TrimSpace(targetVersion) != "" && channel != "" {
		return errors.New("upgrade: specify either a version or --channel, not both")
	}
	if strings.TrimSpace(targetVersion) == "" && channel == "" && version.IsPrereleaseVersion(currentVersion) {
		return fmt.Errorf("upgrade: current version %s is a release candidate; specify a target version or use --channel stable", currentVersion)
	}
	return nil
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

// syncPostgresRuntime fetches the runtime the new binary needs, for a host that
// was already running the embedded database. The binary and its runtime are
// versioned together, so swapping the binary alone leaves a deployment that
// starts up into "no PostgreSQL runtime" — the failure only shows at the next
// restart, which is the worst moment to discover it.
//
// A host with no runtime installed is on the external-database path and is left
// alone; a failure here is reported and not fatal, because the upgrade itself
// has already committed and a missing runtime is recoverable by hand.
func syncPostgresRuntime(ctx context.Context, out io.Writer, installDir, stellaHome, goos string) {
	if !postgresRuntimeInstalled(stellaHome) {
		return
	}
	fprintln(out, "\nUpdating the embedded PostgreSQL runtime for the new version...")
	cmd := exec.CommandContext(ctx, filepath.Join(installDir, binariesToUpgrade(goos)[0]), "postgres", "download")
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		fprintf(out, "\n  Could not update the PostgreSQL runtime: %v\n", err)
		fprintln(out, "  Run `stellad postgres download` before starting the server.")
	}
}

// postgresRuntimeInstalled reports whether this host runs the embedded database.
// Any extracted runtime under the cache root counts, including one for a version
// this binary no longer uses — that is precisely the host that needs the new one.
func postgresRuntimeInstalled(stellaHome string) bool {
	entries, err := os.ReadDir(filepath.Join(stellaHome, "pg-runtime"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
}

// binariesToUpgrade returns the binary names that must be present in a release
// archive for a full upgrade.
func binariesToUpgrade(goos string) []string {
	if goos == "windows" {
		return []string{"stellad.exe"}
	}
	return []string{"stellad"}
}

// fetchRelease fetches a release from GitHub. An empty targetVersion fetches the
// latest stable release; otherwise it fetches the release tagged "v<version>".
func fetchRelease(ctx context.Context, targetVersion string) (*githubRelease, error) {
	base := strings.TrimRight(upgradeAPIBaseURL, "/") + "/repos/" + githubOwner + "/" + githubRepo + "/releases/"
	what := "latest release"
	url := base + "latest"
	if targetVersion != "" {
		what = fmt.Sprintf("release v%s", targetVersion)
		url = base + "tags/v" + targetVersion
	}

	var parsed githubRelease
	resp, err := upgradeHTTPClient.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("User-Agent", upgradeUserAgent).
		SetResult(&parsed).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", what, err)
	}
	if resp.StatusCode() == http.StatusNotFound && targetVersion != "" {
		return nil, fmt.Errorf("release v%s not found — check available versions at https://github.com/%s/%s/releases", targetVersion, githubOwner, githubRepo)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %d: %s", what, resp.StatusCode(), strings.TrimSpace(resp.String()))
	}
	if version.NormalizeVersion(parsed.TagName) == "" {
		return nil, fmt.Errorf("%s tag %q is invalid", what, parsed.TagName)
	}
	return &parsed, nil
}

func runUpgrade(ctx context.Context, out io.Writer, installDir, currentVersion, targetVersion, goos, goarch string) (*upgradeResult, error) {
	targetVersion = version.NormalizeVersion(targetVersion)
	if targetVersion == "" {
		fprintln(out, "Checking for the latest release...")
	} else {
		fprintf(out, "Looking up release v%s...\n", targetVersion)
	}
	release, err := fetchRelease(ctx, targetVersion)
	if err != nil {
		return nil, err
	}

	result := &upgradeResult{
		CurrentVersion: version.NormalizeVersion(currentVersion),
		LatestVersion:  version.NormalizeVersion(release.TagName),
	}
	if result.CurrentVersion == "" {
		result.CurrentVersion = "dev"
	}
	if result.LatestVersion == "" {
		return nil, fmt.Errorf("latest release tag %q is invalid", release.TagName)
	}
	if result.LatestVersion == version.NormalizeVersion(currentVersion) {
		result.AlreadyCurrent = true
		return result, nil
	}

	asset, err := selectReleaseAsset(release.Assets, goos, goarch)
	if err != nil {
		return nil, err
	}

	fprintf(out, "Upgrading stella %s -> %s  (%s/%s)\n", result.CurrentVersion, result.LatestVersion, goos, goarch)
	if err := installReleaseAsset(ctx, out, asset, installDir, goos, release); err != nil {
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

func installReleaseAsset(ctx context.Context, out io.Writer, asset githubReleaseAsset, installDir, goos string, release *githubRelease) (err error) {
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

	archivePath := filepath.Join(tmpDir, filepath.Base(asset.Name))
	fprintf(out, "  Downloading %s\n", asset.Name)
	if err := downloadFile(ctx, out, asset.BrowserDownloadURL, archivePath); err != nil {
		return err
	}

	fprintln(out, "  Verifying checksum...")
	if err := verifyChecksum(ctx, release, asset.Name, archivePath); err != nil {
		return err
	}

	fprintf(out, "  Installing to %s...\n", installDir)
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
			for _, t := range staged {
				_ = os.Remove(t)
			}
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
// On Windows os.Remove can fail because the binary is locked by a running
// process. In that case we rename the locked file to .old — it will be cleaned
// up by cleanStaleUpgradeArtifacts on next startup.
func rollbackUpgrade(committed, backedUp []string) {
	removeFailed := make(map[string]bool, len(committed))
	for _, p := range committed {
		if err := removeFile(p); err != nil {
			// On Windows, try renaming the locked binary out of the way.
			if currentGOOS == "windows" {
				oldPath := p + ".old"
				if renameErr := renameFile(p, oldPath); renameErr == nil {
					_, _ = fmt.Fprintf(os.Stderr, "WARNING: could not remove %s (file locked); renamed to %s — it will be cleaned up on next startup\n", p, oldPath)
					continue
				}
			}
			removeFailed[p] = true
			if currentGOOS == "windows" {
				_, _ = fmt.Fprintf(os.Stderr, "WARNING: could not remove %s: %v\nThe file is likely locked by a running process. Stop all stella processes and manually delete %s, then rename %s.bak to %s\n", p, err, p, p, p)
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "WARNING: could not remove %s: %v\n", p, err)
			}
		}
	}
	for _, p := range backedUp {
		if removeFailed[p] {
			_, _ = fmt.Fprintf(os.Stderr, "WARNING: cannot restore backup — target %s still exists; manually remove it and rename %s.bak\n", p, p)
			continue
		}
		if err := renameFile(p+".bak", p); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "WARNING: failed to restore %s from backup: %v\n", p, err)
		}
	}
}

// verifyChecksum fetches the checksums.txt asset from the release,
// computes the SHA-256 of the downloaded archive, and rejects mismatches.
func verifyChecksum(ctx context.Context, release *githubRelease, archiveName, archivePath string) error {
	csAsset, found := findChecksumAsset(release.Assets)
	if !found {
		slog.Warn("skipping checksum verification — checksums.txt not found in release")
		return nil
	}

	resp, err := upgradeHTTPClient.R().
		SetContext(ctx).
		SetHeader("User-Agent", upgradeUserAgent).
		Get(csAsset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("fetch checksums.txt: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("fetch checksums.txt: unexpected status %d", resp.StatusCode())
	}

	expected, err := parseChecksumForFile(resp.String(), archiveName)
	if err != nil {
		return err
	}

	actual, err := sha256File(archivePath)
	if err != nil {
		return err
	}

	// Hex digests are case-insensitive; some tooling emits uppercase.
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", archiveName, expected, actual)
	}
	return nil
}

func findChecksumAsset(assets []githubReleaseAsset) (githubReleaseAsset, bool) {
	for _, a := range assets {
		name := filepath.Base(a.Name)
		if strings.EqualFold(name, "checksums.txt") || strings.HasSuffix(strings.ToLower(name), "_checksums.txt") {
			return a, true
		}
	}
	return githubReleaseAsset{}, false
}

func parseChecksumForFile(checksumsTxt, filename string) (string, error) {
	for line := range strings.SplitSeq(checksumsTxt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// GoReleaser format: "<sha256>  <filename>"
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == filename {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("no checksum found for %s in checksums.txt", filename)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for checksum: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("compute checksum: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadFile(ctx context.Context, out io.Writer, url, dest string) (err error) {
	resp, err := upgradeDownloadClient.R().
		SetContext(ctx).
		SetHeader("User-Agent", upgradeUserAgent).
		SetDoNotParseResponse(true).
		Get(url)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	body := resp.RawBody()
	defer func() { _ = body.Close() }()
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("download asset: unexpected status %d", resp.StatusCode())
	}

	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close download file: %w", closeErr)
		}
	}()

	bar := newProgressBar(out, resp.RawResponse.ContentLength)
	if _, err := io.Copy(io.MultiWriter(file, bar), body); err != nil {
		bar.abort()
		return fmt.Errorf("download asset: %w", err)
	}
	bar.done()
	return nil
}

// progressBar renders a download progress line that updates in place on a
// terminal. On a non-terminal writer (pipe, CI log) it stays silent so output
// is not flooded with carriage-return spam; the surrounding step messages still
// convey what is happening.
type progressBar struct {
	w       io.Writer
	total   int64 // expected bytes; <=0 when the server sent no Content-Length
	written int64
	tty     bool
	lastPct int
}

func newProgressBar(w io.Writer, total int64) *progressBar {
	return &progressBar{w: w, total: total, tty: isTerminalWriter(w), lastPct: -1}
}

func (b *progressBar) Write(p []byte) (int, error) {
	n := len(p)
	b.written += int64(n)
	b.render()
	return n, nil
}

func (b *progressBar) render() {
	if !b.tty {
		return
	}
	if b.total > 0 {
		pct := min(int(b.written*100/b.total), 100)
		if pct == b.lastPct {
			return
		}
		b.lastPct = pct
		const width = 30
		filled := pct * width / 100
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
		fprintf(b.w, "\r  [%s] %3d%%  %s / %s", bar, pct, humanBytes(b.written), humanBytes(b.total))
		return
	}
	// Unknown size: show a growing byte count, throttled to whole megabytes.
	mb := int(b.written >> 20)
	if mb == b.lastPct {
		return
	}
	b.lastPct = mb
	fprintf(b.w, "\r  %s downloaded", humanBytes(b.written))
}

func (b *progressBar) done() {
	if b.tty {
		fprintln(b.w)
	}
}

// abort moves off the progress line so an error message starts on a fresh line.
func (b *progressBar) abort() {
	if b.tty && b.lastPct >= 0 {
		fprintln(b.w)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// fprintf and fprintln write progress text to the upgrade output stream.
// Output to stdout never meaningfully fails, so the error is dropped — matching
// how the rest of this file treats UI writes.
func fprintf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func fprintln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }

// isTerminalWriter reports whether w is an *os.File attached to a terminal.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// okMark returns a check mark when writing to a terminal, else a plain ASCII
// marker so logs stay readable.
func okMark(w io.Writer) string {
	if isTerminalWriter(w) {
		return "✓"
	}
	return "OK:"
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

	counted := &limitReader{reader: gzReader, remaining: maxArchiveBytes}
	tarReader := tar.NewReader(counted)
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
		if header.Size > maxArchiveBytes {
			return "", fmt.Errorf("archive entry %q exceeds %d MB size limit", header.Name, maxArchiveBytes>>20)
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

type limitReader struct {
	reader    io.Reader
	remaining int64
}

func (r *limitReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fmt.Errorf("decompressed archive exceeds %d MB size limit", maxArchiveBytes>>20)
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
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
		if file.UncompressedSize64 > maxArchiveBytes {
			return "", fmt.Errorf("archive entry %q exceeds %d MB size limit", file.Name, maxArchiveBytes>>20)
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

	// Read one byte past the limit so a payload of exactly maxArchiveBytes is
	// accepted; only a genuine overflow (n > maxArchiveBytes) is rejected. This
	// matches the inclusive `> maxArchiveBytes` guard on tar entry headers.
	limited := io.LimitReader(reader, maxArchiveBytes+1)
	n, err := io.Copy(out, limited)
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("write extracted binary: %w", err)
	}
	if n > maxArchiveBytes {
		_ = out.Close()
		_ = os.Remove(path)
		return fmt.Errorf("extracted binary exceeds %d MB size limit", maxArchiveBytes>>20)
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

// staleUpgradeExtensions are file suffixes left behind by interrupted upgrades.
var staleUpgradeExtensions = []string{".tmp", ".bak", ".old"}

// cleanStaleUpgradeArtifacts removes leftover .tmp and .old files, and treats
// .bak files as rollback state: restore them when the target is missing.
// It is best-effort: failures are logged but never returned.
func cleanStaleUpgradeArtifacts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Debug("cleanStaleUpgradeArtifacts: cannot read dir", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isStaleUpgradeArtifact(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".bak") {
			cleanupBackupArtifact(dir, name, path)
			continue
		}
		if err := removeFile(path); err != nil {
			slog.Warn("cleanStaleUpgradeArtifacts: could not remove stale file", "path", path, "error", err)
		} else {
			slog.Info("cleanStaleUpgradeArtifacts: removed stale upgrade artifact", "path", path)
		}
	}
}

func cleanupBackupArtifact(dir, name, path string) {
	target := filepath.Join(dir, strings.TrimSuffix(name, ".bak"))
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if err := renameFile(path, target); err != nil {
			slog.Warn("cleanStaleUpgradeArtifacts: could not restore backup", "backup", path, "target", target, "error", err)
		} else {
			slog.Warn("cleanStaleUpgradeArtifacts: restored interrupted upgrade backup", "backup", path, "target", target)
		}
		return
	} else if err != nil {
		slog.Warn("cleanStaleUpgradeArtifacts: cannot stat target for backup", "backup", path, "target", target, "error", err)
		return
	}

	// A successful upgrade removes its own .bak (see runUpgrade), so a backup
	// surviving alongside an existing target means a rollback could not restore
	// it. Preserve it and warn — deleting it here would destroy the only
	// recovery artifact the rollback warning told the operator to keep.
	slog.Warn("cleanStaleUpgradeArtifacts: interrupted upgrade left a backup; target still exists — leaving backup in place for manual recovery", "backup", path, "target", target)
}

// warnStaleUpgradeArtifacts checks for leftover upgrade artifacts and logs a
// warning if any are found. Call this early in the server startup path.
func warnStaleUpgradeArtifacts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var stale []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isStaleUpgradeArtifact(e.Name()) {
			stale = append(stale, e.Name())
		}
	}
	if len(stale) > 0 {
		slog.Warn("detected stale upgrade artifacts — a previous upgrade may have been interrupted; cleaning up", "dir", dir, "files", stale)
	}
}

// isStaleUpgradeArtifact returns true if the filename looks like a leftover
// stellad binary from a failed upgrade.
func isStaleUpgradeArtifact(name string) bool {
	for _, ext := range staleUpgradeExtensions {
		if !strings.HasSuffix(name, ext) {
			continue
		}
		base := strings.TrimSuffix(name, ext)
		// Match stellad and stellad.exe.
		if base == "stellad" || base == "stellad.exe" {
			return true
		}
	}
	return false
}
