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
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	archiveName     = "pg-runtime.tar.zst"
	checksumName    = "pg-runtime.sha256"
	versionFileName = ".pg-runtime-sha256"
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
	archive, err := bundleFS.ReadFile(bundleDir + "/" + archiveName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read embedded PostgreSQL runtime: %w", err)
	}

	checksum, err := bundleChecksum(archive)
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
	if _, err := bundleFS.Open(bundleDir + "/" + archiveName); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("embedded PostgreSQL runtime bundle missing for this platform: %s/%s", bundleDir, archiveName)
	} else if err != nil {
		return fmt.Errorf("open embedded PostgreSQL runtime bundle: %w", err)
	}
	return nil
}

func bundleChecksum(archive []byte) (string, error) {
	if data, err := bundleFS.ReadFile(bundleDir + "/" + checksumName); err == nil {
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
