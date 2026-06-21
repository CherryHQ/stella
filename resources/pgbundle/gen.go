//go:build ignore

// gen.go packages a prebuilt Stella PostgreSQL runtime bundle for go:embed.
// Build the runtime first, then run:
//
//	STELLA_PG_RUNTIME_ROOT=/path/to/bundle GOOS=darwin GOARCH=arm64 go run gen.go
//
// The bundle root must contain postgres/bin/pg_ctl and may contain
// extensions/share/extension plus extensions/lib.
package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func main() {
	root := os.Getenv("STELLA_PG_RUNTIME_ROOT")
	if root == "" {
		fatalf("STELLA_PG_RUNTIME_ROOT is required")
	}
	goos := envOr("TARGET_GOOS", envOr("GOOS", runtime.GOOS))
	goarch := envOr("TARGET_GOARCH", envOr("GOARCH", runtime.GOARCH))
	platform := goos + "-" + goarch

	if err := validateRoot(root, goos); err != nil {
		fatalf("invalid runtime root: %v", err)
	}

	outDir := filepath.Join("bundles", platform)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	archivePath := filepath.Join(outDir, "pg-runtime.tar.zst")
	checksumPath := filepath.Join(outDir, "pg-runtime.sha256")
	if err := writeTarZstd(root, archivePath); err != nil {
		fatalf("write archive: %v", err)
	}
	sum, err := fileSHA256(archivePath)
	if err != nil {
		fatalf("checksum archive: %v", err)
	}
	if err := os.WriteFile(checksumPath, []byte(sum+"  pg-runtime.tar.zst\n"), 0o644); err != nil {
		fatalf("write checksum: %v", err)
	}
	fmt.Printf("wrote %s and %s\n", archivePath, checksumPath)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func validateRoot(root, goos string) error {
	pgCtl := "pg_ctl"
	if goos == "windows" {
		pgCtl = "pg_ctl.exe"
	}
	if _, err := os.Stat(filepath.Join(root, "postgres", "bin", pgCtl)); err != nil {
		return fmt.Errorf("missing postgres/bin/%s: %w", pgCtl, err)
	}
	return nil
}

func writeTarZstd(root, archivePath string) error {
	tmp, err := os.CreateTemp(filepath.Dir(archivePath), ".pg-runtime-*.tar.zst")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	zw, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		_ = tmp.Close()
		return err
	}
	tw := tar.NewWriter(zw)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("unsafe path %q", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, archivePath)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pgbundle gen: "+format+"\n", args...)
	os.Exit(1)
}
