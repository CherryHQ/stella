//go:build ignore

// gen.go downloads the mise binary for the target platform and writes it as a
// gzip-compressed file to ./binaries/<platform>/mise.gz. Run via:
//
//	GOOS=<os> GOARCH=<arch> go run gen.go
//
// Defaults to the current runtime platform when GOOS/GOARCH are unset.
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
)

const miseVersion = "2026.7.18"

type miseAsset struct {
	file   string // filename template with {ver} placeholder
	binary string // binary name inside the archive
}

var miseAssets = map[string]miseAsset{
	"darwin-amd64":  {"mise-{ver}-macos-x64.tar.gz", "mise"},
	"darwin-arm64":  {"mise-{ver}-macos-arm64.tar.gz", "mise"},
	"linux-amd64":   {"mise-{ver}-linux-x64.tar.gz", "mise"},
	"linux-arm64":   {"mise-{ver}-linux-arm64.tar.gz", "mise"},
	"windows-amd64": {"mise-{ver}-windows-x64.zip", "mise.exe"},
	"windows-arm64": {"mise-{ver}-windows-arm64.zip", "mise.exe"},
}

func main() {
	goos := os.Getenv("TARGET_GOOS")
	goarch := os.Getenv("TARGET_GOARCH")
	if goos == "" {
		goos = os.Getenv("GOOS")
	}
	if goarch == "" {
		goarch = os.Getenv("GOARCH")
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	platform := goos + "-" + goarch

	spec, ok := miseAssets[platform]
	if !ok {
		fatalf("unsupported platform: %s", platform)
	}

	outDir := filepath.Join("binaries", platform)
	outName := "mise.gz"
	if goos == "windows" {
		outName = "mise.exe.gz"
	}
	outPath := filepath.Join(outDir, outName)
	if cachedVersion(outPath) == miseVersion {
		fmt.Printf("skipping %s (already at %s)\n", outPath, miseVersion)
		return
	}

	ver := "v" + miseVersion
	fileName := strings.ReplaceAll(spec.file, "{ver}", ver)
	url := fmt.Sprintf("https://github.com/jdx/mise/releases/download/%s/%s", ver, fileName)

	tmpDir, err := os.MkdirTemp("", "stella-mise-gen-*")
	if err != nil {
		fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, fileName)
	fmt.Printf("downloading mise %s for %s...\n", miseVersion, platform)
	if err := download(context.Background(), url, archivePath); err != nil {
		fatalf("%v", err)
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		fatalf("create extract dir: %v", err)
	}
	if strings.HasSuffix(fileName, ".tar.gz") {
		if err := extractTarGz(archivePath, extractDir); err != nil {
			fatalf("extract tar.gz: %v", err)
		}
	} else {
		if err := extractZip(archivePath, extractDir); err != nil {
			fatalf("extract zip: %v", err)
		}
	}

	binaryPath, err := findFile(extractDir, spec.binary)
	if err != nil {
		fatalf("find binary in archive: %v", err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	if err := gzipFile(binaryPath, outPath, miseVersion); err != nil {
		fatalf("gzip binary: %v", err)
	}
	fmt.Printf("wrote %s\n", outPath)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen: "+format+"\n", args...)
	os.Exit(1)
}

func download(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	return f.Close()
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		target := filepath.Join(destDir, filepath.Clean(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func extractZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	for _, file := range zr.File {
		target := filepath.Join(destDir, filepath.Clean(file.Name))
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = rc.Close()
		if err := out.Close(); err != nil {
			return err
		}
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func findFile(root, name string) (string, error) {
	var match string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Base(path) == name {
			match = path
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("walk %s: %w", root, err)
	}
	if match == "" {
		return "", fmt.Errorf("%q not found in %s", name, root)
	}
	return match, nil
}

// cachedVersion reports the mise version stamped into an already-generated
// archive, or "" when the archive is missing or unstamped. The version lives in
// the gzip header comment so it travels with the artifact itself and cannot go
// stale independently of it — a bumped miseVersion therefore always forces a
// re-download instead of silently reusing the previous binary.
func cachedVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return ""
	}
	defer func() { _ = gz.Close() }()
	return gz.Header.Comment
}

func gzipFile(src, dst, version string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gz-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	gz, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	gz.Header.Comment = version
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = tmp.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}
