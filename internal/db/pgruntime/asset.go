// Package pgruntime locates, names, and unpacks the bundled PostgreSQL runtime
// asset (versioned tarball, per-source cache directory, checksum URLs).
// internal/db starts the runtime; this package only knows where it lives.
package pgruntime

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	// The -r2 suffix is a rebuild of the same upstream versions: the bundled
	// libraries had no rpath of their own, so a host without a matching system
	// libicu could not start the runtime. The suffix also renames the local
	// cache directory, so an existing install stops using the broken extraction
	// rather than silently keeping it — `stellad upgrade` fetches the
	// replacement, and any other install path needs `postgres download`.
	RuntimeVersion     = "pg18.4-pgvector0.8.2-pgsearch0.24.1-r2"
	DefaultRuntimeRepo = "CherryHQ/stella-pg-runtime"

	supportedLinuxRuntimeSources = "bookworm, noble, trixie"
	stellaIssueURL               = "https://github.com/CherryHQ/stella/issues/new"
)

// MissingRuntimeHint explains why automatic PostgreSQL runtime selection failed.
func MissingRuntimeHint() string {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return fmt.Sprintf("Could not read /etc/os-release to select a Linux runtime. Supported Linux runtime sources: %s. Run `stellad postgres download`, set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue with your OS details: %s", supportedLinuxRuntimeSources, stellaIssueURL)
		}
		codename := linuxRuntimeCodenameFromOSRelease(string(data))
		if codename == "" {
			return fmt.Sprintf("Could not detect VERSION_CODENAME or UBUNTU_CODENAME from /etc/os-release. Supported Linux runtime sources: %s. Run `stellad postgres download`, set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue with your /etc/os-release: %s", supportedLinuxRuntimeSources, stellaIssueURL)
		}
		if _, ok := supportedLinuxRuntimeSource(codename); !ok {
			return fmt.Sprintf("Detected Linux runtime source %q, but Stella only publishes PostgreSQL runtimes for: %s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue requesting this distro: %s", codename, supportedLinuxRuntimeSources, stellaIssueURL)
		}
		return fmt.Sprintf("Detected supported Linux runtime source %q, but no PostgreSQL runtime is installed. Run `stellad postgres download`, set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue if download fails: %s", codename, stellaIssueURL)
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return fmt.Sprintf("No PostgreSQL runtime is installed for darwin/arm64. Run `stellad postgres download`, set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue if download fails: %s", stellaIssueURL)
		}
		return fmt.Sprintf("PostgreSQL runtime downloads are not published for darwin/%s. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue for this platform: %s", runtime.GOARCH, stellaIssueURL)
	default:
		return fmt.Sprintf("PostgreSQL runtime downloads are currently published for linux/amd64|arm64 (%s) and darwin/arm64. Set STELLA_DATABASE_URL to an external PostgreSQL with pg_search and pgvector, or file an issue for this platform: %s", supportedLinuxRuntimeSources, stellaIssueURL)
	}
}

func DefaultRuntimeSource() (string, bool) {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "postgresapp", true
		}
	case "linux":
		return linuxRuntimeSource()
	}
	return "", false
}

func RuntimeRoot(stellaHome, source string) string {
	return filepath.Join(runtimesDir(stellaHome), CurrentRuntimeDir(), "downloaded", source)
}

// CurrentRuntimeDir names the directory holding the runtime this binary uses.
// Every installed version gets its own sibling, so the name doubles as the
// identity an operator sees when deciding what is safe to remove.
func CurrentRuntimeDir() string {
	return RuntimeVersion + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

func runtimesDir(stellaHome string) string {
	return filepath.Join(stellaHome, "pg-runtime")
}

// InstalledRuntime is one extracted runtime version found on disk.
type InstalledRuntime struct {
	// Name is the directory name, which encodes version, OS, and architecture.
	Name string
	Path string
	// Bytes is the extracted size, best effort: entries that cannot be read are
	// skipped rather than failing a report whose only purpose is disk space.
	Bytes int64
	// Current marks the runtime this binary would use. Removing it is not
	// pruning, it is uninstalling, so callers keep the two apart.
	Current bool
}

// InstalledRuntimes lists the runtime versions extracted under $STELLA_HOME,
// sorted by name. A missing pg-runtime directory is not an error: it just means
// nothing has been downloaded yet.
func InstalledRuntimes(stellaHome string) ([]InstalledRuntime, error) {
	dir := runtimesDir(stellaHome)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL runtime dir %s: %w", dir, err)
	}
	current := CurrentRuntimeDir()
	installed := make([]InstalledRuntime, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		installed = append(installed, InstalledRuntime{
			Name:    entry.Name(),
			Path:    path,
			Bytes:   dirSize(path),
			Current: entry.Name() == current,
		})
	}
	slices.SortFunc(installed, func(a, b InstalledRuntime) int { return strings.Compare(a.Name, b.Name) })
	return installed, nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		// Only regular files consume space here; a symlink's target is either
		// inside the tree already or outside it and not ours to count.
		if err == nil && d.Type().IsRegular() {
			if info, statErr := d.Info(); statErr == nil {
				total += info.Size()
			}
		}
		// Unreadable entries are skipped rather than aborting: this number only
		// tells an operator roughly how much a prune would free.
		return nil
	})
	return total
}

func RuntimeAssetName(version, goos, goarch, source string) string {
	return fmt.Sprintf("stella-pg-runtime-%s-%s-%s-%s.tar.zst", version, goos, goarch, source)
}

func RuntimeAssetURL(repo, version, goos, goarch, source string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, version, RuntimeAssetName(version, goos, goarch, source))
}

func RuntimeChecksumURL(repo, version, goos, goarch, source string) string {
	return RuntimeAssetURL(repo, version, goos, goarch, source) + ".sha256"
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
	// Runtime archives are built from distro packages; do not guess across distro
	// families because glibc and extension ABI mismatches fail later and uglier.
	switch codename {
	case "bookworm", "noble", "trixie":
		return codename, true
	default:
		return "", false
	}
}

func ExtractTarZstdFile(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open PostgreSQL runtime archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	return extractTarZstdReader(f, dest)
}

func extractTarZstdReader(r io.Reader, dest string) error {
	zr, err := zstd.NewReader(r)
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
