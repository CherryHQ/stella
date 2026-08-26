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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	miseVersion = "2026.8.6"
	// Keep synchronized with xbergVersion in embedded.go; the generated archive
	// filename is the contract between this build helper and runtime extraction.
	xbergVersion = "1.0.14"
)

type miseAsset struct {
	file   string // filename template with {ver} placeholder
	binary string // binary name inside the archive
	sha256 string // upstream SHASUMS256.txt entry for this asset
}

// Checksums come from the release's SHASUMS256.txt. mise bootstraps every other
// tool, so an unverified download here is the highest-privilege hole in the
// chain; it must be pinned as tightly as Xberg.
var miseAssets = map[string]miseAsset{
	"darwin-amd64":  {"mise-{ver}-macos-x64.tar.gz", "mise", "925d242515338975071daab9e23bcf5803a0eafa2b8e9b4f78f61af5b4c865d5"},
	"darwin-arm64":  {"mise-{ver}-macos-arm64.tar.gz", "mise", "14ef21d1313d3b69986ac6976877d7ffb41df71f4fb9e8e4b57761cffaffca3b"},
	"linux-amd64":   {"mise-{ver}-linux-x64.tar.gz", "mise", "cfe49784ec9683b38510846958cfecd9b59da84d4e8a38d18ffda19dc2941ead"},
	"linux-arm64":   {"mise-{ver}-linux-arm64.tar.gz", "mise", "b92744ceb9a01f0bb198bfcf2ba49c36918c9e4353a34be50f23d5b6e93c28ee"},
	"windows-amd64": {"mise-{ver}-windows-x64.zip", "mise.exe", "54b43c2b03825d3b56798d2dacad515deca5ddb1cc9050b38e1601c0d35a3930"},
	"windows-arm64": {"mise-{ver}-windows-arm64.zip", "mise.exe", "f6fabb8ce562a8ba836d6fefff22c526fb973f7fba22209f31ae569efe79332c"},
}

type xbergAsset struct {
	file   string
	sha256 string
}

var xbergAssets = map[string]xbergAsset{
	"darwin-amd64": {"xberg-cli-x86_64-apple-darwin.tar.gz", "5ab204912e9585a82790b2b0345f0cd0224bc384047ea831488d85eb0f5e4d2a"},
	"darwin-arm64": {"xberg-cli-aarch64-apple-darwin.tar.gz", "5e275783ddd21071d3568c523cec61e739b2c3a601ea8042862372bf362ecb77"},
	"linux-amd64":  {"xberg-cli-x86_64-unknown-linux-gnu.tar.gz", "ca1f61edc8ef274619c676bd96b05800872c88abfaace3d067496f7841a7364c"},
	"linux-arm64":  {"xberg-cli-aarch64-unknown-linux-gnu.tar.gz", "bab4fadcbdb7d9bac17409efd041808b67a3f0767525481642b2b9f62c4f1fc6"},
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

	if _, ok := miseAssets[platform]; !ok {
		fatalf("unsupported platform: %s", platform)
	}
	syncMise(platform, goos)
	syncXberg(platform)
}

func syncMise(platform, goos string) {
	spec := miseAssets[platform]

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
	// Verify before unpacking: archive extraction is the first code path that
	// acts on attacker-controlled bytes.
	if got := fileSHA256(archivePath); got != spec.sha256 {
		fatalf("verify mise archive: SHA-256 %s, want %s", got, spec.sha256)
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

func syncXberg(platform string) {
	spec, ok := xbergAssets[platform]
	if !ok {
		fmt.Printf("skipping embedded Xberg for unsupported platform %s\n", platform)
		return
	}
	outDir := filepath.Join("binaries", platform)
	// A fixed filename is what lets embed_*.go name this artifact exactly, so a
	// build with no generated archive fails to compile instead of silently
	// embedding nothing. The version therefore cannot live in the filename; it
	// rides in the gzip header comment, the same way mise's does, where it cannot
	// go stale independently of the bytes it describes.
	outPath := filepath.Join(outDir, "xberg.tar.gz")
	if cachedVersion(outPath) == xbergVersion {
		fmt.Printf("skipping %s (already at %s)\n", outPath, xbergVersion)
		return
	}
	for _, stale := range mustGlob(filepath.Join(outDir, "xberg-v*.tar.gz")) {
		if err := os.Remove(stale); err != nil {
			fatalf("remove stale Xberg bundle %s: %v", stale, err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "stella-xberg-gen-*")
	if err != nil {
		fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	url := fmt.Sprintf("https://github.com/xberg-io/xberg/releases/download/v%s/%s", xbergVersion, spec.file)
	downloaded := filepath.Join(tmpDir, spec.file)
	fmt.Printf("downloading Xberg %s for %s...\n", xbergVersion, platform)
	if err := download(context.Background(), url, downloaded); err != nil {
		fatalf("%v", err)
	}
	// Verify the bytes upstream published, before rewriting the gzip envelope.
	if got := fileSHA256(downloaded); got != spec.sha256 {
		fatalf("verify Xberg bundle: SHA-256 %s, want %s", got, spec.sha256)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	if err := regzipWithVersion(downloaded, outPath, xbergVersion); err != nil {
		fatalf("stamp Xberg bundle version: %v", err)
	}
	fmt.Printf("wrote %s\n", outPath)
}

// regzipWithVersion rewrites a gzip stream so its header carries the version.
// Upstream ships a plain .tar.gz, and a gzip comment cannot be added without
// recompressing, so this costs one decompress/compress pass per version bump at
// generate time. The tar payload itself is untouched.
func regzipWithVersion(src, dst, version string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	gr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer func() { _ = gr.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".xberg-*")
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
	if _, err := io.Copy(gz, gr); err != nil {
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

func mustGlob(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		fatalf("glob %s: %v", pattern, err)
	}
	return matches
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
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

// safeJoin resolves an archive entry name inside destDir. filepath.Clean alone
// does not do this: it preserves a leading "..", so Join happily produces a path
// outside the destination. Checksums make a malicious archive unlikely, not
// impossible — a wrong pin protects nothing on its own.
func safeJoin(destDir, name string) (string, error) {
	target := filepath.Join(destDir, filepath.Clean("/"+name))
	rel, err := filepath.Rel(destDir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return target, nil
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
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
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
		target, err := safeJoin(destDir, file.Name)
		if err != nil {
			return err
		}
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
