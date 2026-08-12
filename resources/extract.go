package resources

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ExtractDelegates writes builtin delegate preset files into delegatesDir.
// Individual files are overwritten; other content in delegatesDir is preserved.
func ExtractDelegates(delegatesDir string) error {
	sub, ok := SubFS(KindDelegate)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(delegatesDir, 0o755); err != nil {
		return fmt.Errorf("create delegates dir: %w", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return fmt.Errorf("read builtin delegates: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(sub, entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(delegatesDir, entry.Name()), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", entry.Name(), err)
		}
	}
	return nil
}
