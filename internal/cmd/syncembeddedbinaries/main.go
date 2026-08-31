// Command syncembeddedbinaries downloads the third-party runtimes that get
// compiled into stellad and writes them to resources/binaries/binaries/<platform>/.
//
// It lives outside resources/binaries on purpose. embed_*.go names each archive
// exactly so a build without the artifacts fails to compile rather than silently
// shipping none; a generator inside that package could then never run, because
// `go generate` must load the package first. Run it via `mise run generate`, or:
//
//	TARGET_GOOS=<os> TARGET_GOARCH=<arch> go run ./internal/cmd/syncembeddedbinaries
//
// Syncs all supported embedded platforms when no target is set, or one target
// when TARGET_GOOS and TARGET_GOARCH are provided.
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
	miseVersion = "2026.8.14"
	// Runtime reads the version back from the archive's gzip header comment, so
	// this constant is the single place a bump happens. The generated *filename*
	// carries no version: it is the contract between this helper and the exact
	// //go:embed patterns in resources/binaries.
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
	"darwin-amd64":  {"mise-{ver}-macos-x64.tar.gz", "mise", "6085d0b7c7bf8e176397c48e3f1e2025bd41d69dd50f05c08cb7ae89fb7f77b1"},
	"darwin-arm64":  {"mise-{ver}-macos-arm64.tar.gz", "mise", "e3ba526b629c41fa7b0918f78e746ca71a7a4b0c78dbfaca9fb25676a318762e"},
	"linux-amd64":   {"mise-{ver}-linux-x64.tar.gz", "mise", "64d5f34aeb7a4e0e327dc1c9be66cd8162e14899a47b11901154a100285a3d61"},
	"linux-arm64":   {"mise-{ver}-linux-arm64.tar.gz", "mise", "940639580227bd838e3b3ea5b2084ea397399b0db162c2e4dd90b5730850e48e"},
	"windows-amd64": {"mise-{ver}-windows-x64.zip", "mise.exe", "dfae38ae7c782ef2f0255aa2cd7f8d2c242559f94c491fb59182c7ad36c2bf79"},
	"windows-arm64": {"mise-{ver}-windows-arm64.zip", "mise.exe", "135d747cfc8a1e522340b0afaa4d79681cf7ff9d3f612b928d323aad6c11c59f"},
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

// outputRoot is the embed source directory, relative to the module root that
// `go run ./internal/cmd/...` sets as the working directory.
const outputRoot = "resources/binaries/binaries"

var embeddedPlatforms = []string{
	"darwin-amd64",
	"darwin-arm64",
	"linux-amd64",
	"linux-arm64",
	"windows-amd64",
	"windows-arm64",
}

func main() {
	targetGOOS := os.Getenv("TARGET_GOOS")
	targetGOARCH := os.Getenv("TARGET_GOARCH")
	goos := targetGOOS
	goarch := targetGOARCH
	if goos == "" {
		goos = os.Getenv("GOOS")
	}
	if goarch == "" {
		goarch = os.Getenv("GOARCH")
	}
	if targetGOOS == "" && targetGOARCH == "" && goos == "" && goarch == "" {
		for _, platform := range embeddedPlatforms {
			parts := strings.SplitN(platform, "-", 2)
			syncPlatform(parts[0], parts[1])
		}
		return
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	syncPlatform(goos, goarch)
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		syncPlatform(runtime.GOOS, runtime.GOARCH)
	}
}

func syncPlatform(goos, goarch string) {
	platform := goos + "-" + goarch
	if _, ok := miseAssets[platform]; !ok {
		fatalf("unsupported platform: %s", platform)
	}
	syncMise(platform, goos)
	syncXberg(platform)
}

func syncMise(platform, goos string) {
	spec := miseAssets[platform]

	outDir := filepath.Join(outputRoot, platform)
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
	outDir := filepath.Join(outputRoot, platform)
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
	gz.Comment = version
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
	return gz.Comment
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
	gz.Comment = version
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
