package embedded

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

var ensureOnce sync.Once

// EnsureTools extracts all embedded tool binaries to annaHome/bin/.
// Gzip-compressed binaries in the embed FS are decompressed on extraction.
// Already-extracted binaries are skipped. Safe for concurrent calls.
func EnsureTools(annaHome string) error {
	var err error
	ensureOnce.Do(func() {
		err = extractTools(BinDir(annaHome))
	})
	return err
}

// BinDir returns the tool binaries directory path.
func BinDir(annaHome string) string {
	return filepath.Join(annaHome, "bin")
}

// ToolPath returns the full path to a named tool binary, or empty if not embedded.
func ToolPath(annaHome, name string) string {
	p := filepath.Join(BinDir(annaHome), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ToolNames returns the names of all embedded tools for the current platform.
func ToolNames() []string {
	entries, err := fs.ReadDir(toolsFS, toolsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			names = append(names, strings.TrimSuffix(e.Name(), ".gz"))
		}
	}
	return names
}

func extractTools(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	entries, err := fs.ReadDir(toolsFS, toolsDir)
	if err != nil {
		return fmt.Errorf("read embedded tools: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".gz") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".gz")
		destPath := filepath.Join(destDir, name)

		if _, err := os.Stat(destPath); err == nil {
			continue // already extracted
		}

		if err := extractGzip(toolsDir+"/"+entry.Name(), destPath); err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
		}
	}
	return nil
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

	if _, err = io.Copy(f, gr); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
