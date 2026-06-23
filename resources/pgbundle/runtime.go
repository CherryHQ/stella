package pgbundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	archiveName     = "pg-runtime.tar.zst"
	checksumName    = "pg-runtime.sha256"
	versionFileName = ".pg-runtime-sha256"

	supportedLinuxRuntimeSources = "bookworm, noble, trixie"
	stellaIssueURL               = "https://github.com/CherryHQ/stella/issues/new"
)

// EnsureBundle extracts the embedded PostgreSQL runtime bundle into cacheRoot and
// returns the extracted bundle root. Development builds may omit the archive; in
// that case ok is false and callers can fall back to embedded-postgres' default
// downloader. Release builds should call VerifyBundlePresent to enforce that the
// platform archive was embedded.
func EnsureBundle(cacheRoot string) (root string, ok bool, err error) {
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", false, fmt.Errorf("create PostgreSQL runtime cache: %w", err)
	}
	archivePath, checksumPath, ok, err := selectBundlePaths()
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	archive, err := bundleFS.ReadFile(archivePath)
	if err != nil {
		return "", false, fmt.Errorf("read embedded PostgreSQL runtime: %w", err)
	}

	checksum, err := bundleChecksum(archive, checksumPath)
	if err != nil {
		return "", false, err
	}
	dest := filepath.Join(cacheRoot, checksum)
	stamp := filepath.Join(dest, versionFileName)
	if old, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(old)) == checksum {
		return dest, true, nil
	}

	tmp, err := os.MkdirTemp(cacheRoot, ".pg-runtime-*")
	if err != nil {
		return "", false, fmt.Errorf("create PostgreSQL runtime temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := extractTarZstd(archive, tmp); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(filepath.Join(tmp, versionFileName), []byte(checksum), 0o644); err != nil {
		return "", false, fmt.Errorf("write PostgreSQL runtime stamp: %w", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return "", false, fmt.Errorf("replace PostgreSQL runtime bundle: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", false, fmt.Errorf("install PostgreSQL runtime bundle: %w", err)
	}
	return dest, true, nil
}

func VerifyBundlePresent() error {
	archivePath, _, ok, err := selectBundlePaths()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("embedded PostgreSQL runtime bundle missing for this platform: %s/%s. %s", bundleDir, archiveName, MissingBundleHint())
	}
	if _, err := bundleFS.Open(archivePath); err != nil {
		return fmt.Errorf("open embedded PostgreSQL runtime bundle: %w", err)
	}
	return nil
}

func selectBundlePaths() (archivePath, checksumPath string, ok bool, err error) {
	archivePath = bundleDir + "/" + archiveName
	checksumPath = bundleDir + "/" + checksumName
	if _, err := bundleFS.Open(archivePath); err == nil {
		return archivePath, checksumPath, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", false, fmt.Errorf("open embedded PostgreSQL runtime bundle: %w", err)
	}

	if runtime.GOOS != "linux" {
		return "", "", false, nil
	}
	source, ok := linuxRuntimeSource()
	if !ok {
		return "", "", false, nil
	}
	archivePath = bundleDir + "/" + source + "/" + archiveName
	checksumPath = bundleDir + "/" + source + "/" + checksumName
	if _, err := bundleFS.Open(archivePath); errors.Is(err, fs.ErrNotExist) {
		return "", "", false, nil
	} else if err != nil {
		return "", "", false, fmt.Errorf("open embedded PostgreSQL runtime bundle: %w", err)
	}
	return archivePath, checksumPath, true, nil
}

// MissingBundleHint explains why automatic embedded PostgreSQL selection failed.
func MissingBundleHint() string {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return fmt.Sprintf("Could not read /etc/os-release to select a Linux runtime bundle. Supported Linux runtime sources: %s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue with your OS details: %s", supportedLinuxRuntimeSources, stellaIssueURL)
		}
		codename := linuxRuntimeCodenameFromOSRelease(string(data))
		if codename == "" {
			return fmt.Sprintf("Could not detect VERSION_CODENAME or UBUNTU_CODENAME from /etc/os-release. Supported Linux runtime sources: %s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue with your /etc/os-release: %s", supportedLinuxRuntimeSources, stellaIssueURL)
		}
		if _, ok := supportedLinuxRuntimeSource(codename); !ok {
			return fmt.Sprintf("Detected Linux runtime source %q, but Stella only embeds PostgreSQL runtimes for: %s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue requesting this distro: %s", codename, supportedLinuxRuntimeSources, stellaIssueURL)
		}
		return fmt.Sprintf("Detected supported Linux runtime source %q, but this binary does not contain its PostgreSQL runtime bundle. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector; if this is an official Stella release, file a packaging bug: %s", codename, stellaIssueURL)
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return fmt.Sprintf("This darwin/arm64 binary does not contain its PostgreSQL runtime bundle. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector; if this is an official Stella release, file a packaging bug: %s", stellaIssueURL)
		}
		return fmt.Sprintf("Embedded PostgreSQL release bundles are not published for darwin/%s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue for this platform: %s", runtime.GOARCH, stellaIssueURL)
	default:
		return fmt.Sprintf("Embedded PostgreSQL release bundles are currently published for linux/amd64|arm64 (%s) and darwin/arm64. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue for this platform: %s", supportedLinuxRuntimeSources, stellaIssueURL)
	}
}

func linuxRuntimeSource() (string, bool) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", false
	}
	return linuxRuntimeSourceFromOSRelease(string(data))
}

func linuxRuntimeSourceFromOSRelease(data string) (string, bool) {
	return supportedLinuxRuntimeSource(linuxRuntimeCodenameFromOSRelease(data))
}

func linuxRuntimeCodenameFromOSRelease(data string) string {
	values := map[string]string{}
	for line := range strings.SplitSeq(data, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, "\"'")
		values[key] = value
	}
	if codename := values["VERSION_CODENAME"]; codename != "" {
		return codename
	}
	return values["UBUNTU_CODENAME"]
}

func supportedLinuxRuntimeSource(codename string) (string, bool) {
	// Runtime bundles are built from distro packages; do not guess across distro
	// families because glibc and extension ABI mismatches fail later and uglier.
	switch codename {
	case "bookworm", "noble", "trixie":
		return codename, true
	default:
		return "", false
	}
}

func bundleChecksum(archive []byte, checksumPath string) (string, error) {
	if data, err := bundleFS.ReadFile(checksumPath); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			return fields[0], nil
		}
		return "", fmt.Errorf("embedded PostgreSQL runtime checksum file is empty")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read embedded PostgreSQL runtime checksum: %w", err)
	}
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:]), nil
}

func extractTarZstd(archive []byte, dest string) error {
	zr, err := zstd.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open PostgreSQL runtime zstd archive: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read PostgreSQL runtime tar archive: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe PostgreSQL runtime archive path %q", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("create PostgreSQL runtime dir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create PostgreSQL runtime parent %s: %w", target, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create PostgreSQL runtime file %s: %w", target, err)
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("write PostgreSQL runtime file %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close PostgreSQL runtime file %s: %w", target, closeErr)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create PostgreSQL runtime symlink parent %s: %w", target, err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("create PostgreSQL runtime symlink %s: %w", target, err)
			}
		}
	}
}
