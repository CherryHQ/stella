package binaries

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type ensureState struct {
	once sync.Once
	err  error
}

var (
	ensureMu     sync.Mutex
	ensureStates = make(map[string]*ensureState)
)

// EnsureTools extracts all embedded tool binaries to stellaHome/bin/.
// Gzip-compressed binaries in the embed FS are decompressed on extraction.
// Already-extracted binaries are skipped. Safe for concurrent calls.
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
		name, ok := toolNameForEntry(e.Name())
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
		name, ok := toolNameForEntry(e.Name())
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
		name, ok := toolNameForEntry(entry.Name())
		if !ok {
			continue
		}
		destPath := filepath.Join(destDir, name)

		if err := extractGzip(toolsDir+"/"+entry.Name(), destPath); err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
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
		if _, ok := toolNameForEntry(entry.Name()); ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func toolNameForEntry(entry string) (string, bool) {
	if !strings.HasSuffix(entry, ".gz") {
		return "", false
	}
	return strings.TrimSuffix(entry, ".gz"), true
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
