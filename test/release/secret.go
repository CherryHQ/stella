package release

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const secretScanChunkSize = 32 * 1024

type secretPattern struct {
	name  string
	value []byte
}

// ScanForSecrets scans every regular file and path below root without
// following symbolic links. Errors identify only the secret variable name,
// never its value.
func ScanForSecrets(root string, secrets map[string]string) error {
	patterns, err := compileSecretPatterns(secrets)
	if err != nil || len(patterns) == 0 {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect secret scan root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("secret scan root must be a real directory")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve secret scan path: %w", err)
		}
		if relative != "." {
			if name := findSecret([]byte(filepath.ToSlash(relative)), patterns); name != "" {
				// Do not print the path here: it contains the secret value that
				// triggered this branch.
				return fmt.Errorf("secret %s appears in an artifact path", name)
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("secret scan refuses symbolic link %s", filepath.ToSlash(relative))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect secret scan file %s: %w", filepath.ToSlash(relative), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("secret scan refuses non-regular file %s", filepath.ToSlash(relative))
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open secret scan file %s: %w", filepath.ToSlash(relative), err)
		}
		name, scanErr := scanReader(file, patterns)
		closeErr := file.Close()
		if scanErr != nil {
			return fmt.Errorf("scan file %s: %w", filepath.ToSlash(relative), scanErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close secret scan file %s: %w", filepath.ToSlash(relative), closeErr)
		}
		if name != "" {
			return fmt.Errorf("secret %s appears in artifact file %s", name, filepath.ToSlash(relative))
		}
		return nil
	})
}

// CheckBytesForSecrets scans generated in-memory output before it is installed
// on disk.
func CheckBytesForSecrets(label string, data []byte, secrets map[string]string) error {
	patterns, err := compileSecretPatterns(secrets)
	if err != nil {
		return err
	}
	if name := findSecret(data, patterns); name != "" {
		return fmt.Errorf("secret %s appears in generated %s", name, label)
	}
	return nil
}

func compileSecretPatterns(secrets map[string]string) ([]secretPattern, error) {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	patterns := make([]secretPattern, 0, len(names))
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("secret name cannot be empty")
		}
		if secrets[name] == "" {
			return nil, fmt.Errorf("secret %s cannot be empty", name)
		}
		patterns = append(patterns, secretPattern{name: name, value: []byte(secrets[name])})
	}
	return patterns, nil
}

func scanReader(reader io.Reader, patterns []secretPattern) (string, error) {
	maxPattern := 0
	for _, pattern := range patterns {
		if len(pattern.value) > maxPattern {
			maxPattern = len(pattern.value)
		}
	}
	buffer := make([]byte, secretScanChunkSize)
	var carry []byte
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			window := make([]byte, 0, len(carry)+n)
			window = append(window, carry...)
			window = append(window, buffer[:n]...)
			if name := findSecret(window, patterns); name != "" {
				return name, nil
			}
			keep := min(maxPattern-1, len(window))
			carry = append(carry[:0], window[len(window)-keep:]...)
		}
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
	}
}

func findSecret(data []byte, patterns []secretPattern) string {
	for _, pattern := range patterns {
		if bytes.Contains(data, pattern.value) {
			return pattern.name
		}
	}
	return ""
}
