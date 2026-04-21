package builddeps

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTarGz extracts a .tar.gz archive into destDir.
func ExtractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		target, err := secureJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkPath(destDir, target); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for %s: %w", target, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("close %s: %w", target, err)
			}
		}
	}
}

// ExtractZip extracts a .zip archive into destDir.
func ExtractZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	for _, file := range zr.File {
		target, err := secureJoin(destDir, file.Name)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkPath(destDir, target); err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir parent for %s: %w", target, err)
		}
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", file.Name, err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("create %s: %w", target, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			_ = out.Close()
			_ = rc.Close()
			return fmt.Errorf("write %s: %w", target, err)
		}
		if err := out.Close(); err != nil {
			_ = rc.Close()
			return fmt.Errorf("close %s: %w", target, err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("close %s: %w", file.Name, err)
		}
	}
	return nil
}

func secureJoin(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || clean == string(filepath.Separator) {
		return root, nil
	}
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("archive path escapes destination: %s", name)
	}
	return filepath.Join(root, clean), nil
}

func ensureNoSymlinkPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root path: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("rel path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target escapes destination: %s", target)
	}
	current := rootAbs
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("lstat %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path not allowed during extraction: %s", current)
		}
	}
	return nil
}
