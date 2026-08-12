package binaries

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Keep synchronized with xbergVersion in gen.go.
const xbergVersion = "1.0.14"

type ensureState struct {
	once sync.Once
	err  error
}

var (
	ensureMu     sync.Mutex
	ensureStates = make(map[string]*ensureState)
)

// EnsureTools extracts Stella's embedded runtimes to stellaHome/bin/. mise is a
// single compressed executable; Xberg is kept as a directory because its
// official Linux and macOS bundles include adjacent dynamic libraries.
// Already-extracted runtimes are skipped. Safe for concurrent calls.
func EnsureTools(stellaHome string) error {
	destDir := BinDir(stellaHome)

	ensureMu.Lock()
	state := ensureStates[destDir]
	if state == nil {
		state = &ensureState{}
		ensureStates[destDir] = state
	}
	ensureMu.Unlock()

	state.once.Do(func() {
		state.err = extractTools(destDir)
	})
	return state.err
}

// BinDir returns the tool binaries directory path.
func BinDir(stellaHome string) string {
	return filepath.Join(stellaHome, "bin")
}

// ToolPath returns the full path to a named tool binary, or empty if not embedded.
func ToolPath(stellaHome, name string) string {
	p := filepath.Join(BinDir(stellaHome), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ToolNames returns the names of all embedded tools for the current platform.
func ToolNames() []string {
	entries, err := platformEntries()
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name, ok := embeddedToolName(e.Name())
		if ok {
			names = append(names, name)
		}
	}
	return names
}

// VerifyTools checks that every tool present in the embedded FS was successfully
// extracted to stellaHome/bin. If the embedded FS has no tool archives (e.g. a
// dev build where pre-build dependency sync was not run), the check is skipped and nil is
// returned — the missing-binary error surfaces later when the tool is actually
// needed. Returns an error only when the FS is non-empty but one or more
// binaries are missing on disk after extraction.
func VerifyTools(stellaHome string) error {
	names := ToolNames()
	if len(names) == 0 {
		return nil // no tools embedded; skip verification
	}
	var missing []string
	for _, name := range names {
		if ToolPath(stellaHome, name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("embedded tools missing in %s after extraction: %s",
			BinDir(stellaHome), strings.Join(missing, ", "))
	}
	return nil
}

func allToolsExtracted(destDir string, entries []fs.DirEntry) bool {
	for _, e := range entries {
		name, ok := embeddedToolName(e.Name())
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(destDir, name)); err != nil {
			return false
		}
	}
	return true
}

func extractTools(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	entries, err := platformEntries()
	if err != nil {
		return fmt.Errorf("read embedded tools: %w", err)
	}

	fp := fingerprint()
	fpFile := filepath.Join(destDir, ".embedded-version")
	if old, err := os.ReadFile(fpFile); err == nil && string(old) == fp {
		if allToolsExtracted(destDir, entries) {
			return nil // already up to date
		}
	}

	for _, entry := range entries {
		name := entry.Name()
		if tool, ok := toolNameForEntry(name); ok {
			if err := extractGzip(toolsDir+"/"+name, filepath.Join(destDir, tool)); err != nil {
				return fmt.Errorf("extract %s: %w", tool, err)
			}
			continue
		}
		if name == xbergArchiveName() {
			if err := extractXbergBundle(toolsDir+"/"+name, destDir); err != nil {
				return fmt.Errorf("extract Xberg: %w", err)
			}
		}
	}

	return os.WriteFile(fpFile, []byte(fp), 0o644)
}

// fingerprint returns a quick identifier based on embedded file names and sizes.
// Changes when tool versions are bumped (different binary sizes).
func fingerprint() string {
	entries, err := platformEntries()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d,", e.Name(), info.Size())
		}
	}
	return b.String()
}

func platformEntries() ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(toolsFS, toolsDir)
	if err != nil {
		return nil, err
	}
	var filtered []fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := embeddedToolName(entry.Name()); ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// infraTools lists standalone gzip binaries extracted to $STELLA_HOME/bin.
// Only mise belongs here: it bootstraps the install/shim machinery before any
// shim exists. Xberg is handled separately as a versioned runtime bundle;
// ordinary tools (gh, fd, rg, tap, lark-cli, rtk, ...) stay behind mise shims.
var infraTools = map[string]bool{"mise": true}

func xbergArchiveName() string { return "xberg-v" + xbergVersion + ".tar.gz" }

func embeddedToolName(entry string) (string, bool) {
	if entry == xbergArchiveName() {
		return "xberg", true
	}
	return toolNameForEntry(entry)
}

func toolNameForEntry(entry string) (string, bool) {
	if !strings.HasSuffix(entry, ".gz") {
		return "", false
	}
	name := strings.TrimSuffix(entry, ".gz")
	// On Windows the embedded entry is "mise.exe.gz"; infraTools keys are the
	// platform-neutral tool name, so strip a trailing ".exe" before the lookup.
	if !infraTools[strings.TrimSuffix(name, ".exe")] {
		return "", false
	}
	return name, true
}

func extractGzip(srcPath, destPath string) error {
	data, err := toolsFS.ReadFile(srcPath)
	if err != nil {
		return err
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gr.Close() }()

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	const maxBinarySize = 200 << 20 // 200 MB safety cap
	if _, err = io.Copy(f, io.LimitReader(gr, maxBinarySize)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func extractXbergBundle(srcPath, destDir string) error {
	data, err := toolsFS.ReadFile(srcPath)
	if err != nil {
		return err
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gr.Close() }()

	runtimeDir := filepath.Join(destDir, "xberg-v"+xbergVersion)
	tmpDir, err := os.MkdirTemp(destDir, ".xberg-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	const maxBundleSize = 300 << 20
	var written int64
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(filepath.Clean(h.Name))
		if name == "." || name == string(filepath.Separator) {
			return fmt.Errorf("invalid bundle entry %q", h.Name)
		}
		if h.Size < 0 || written+h.Size > maxBundleSize {
			return fmt.Errorf("bundle exceeds %d bytes", maxBundleSize)
		}
		mode := os.FileMode(0o644)
		if name == "xberg" {
			mode = 0o755
		}
		out, err := os.OpenFile(filepath.Join(tmpDir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, tr, h.Size)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		written += h.Size
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "xberg")); err != nil {
		return fmt.Errorf("bundle executable missing: %w", err)
	}
	if err := os.RemoveAll(runtimeDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, runtimeDir); err != nil {
		return err
	}
	launcher := filepath.Join(destDir, "xberg")
	launcherTmp := launcher + ".tmp"
	_ = os.Remove(launcherTmp)
	if err := os.Symlink(filepath.Join(filepath.Base(runtimeDir), "xberg"), launcherTmp); err != nil {
		return err
	}
	return os.Rename(launcherTmp, launcher)
}
