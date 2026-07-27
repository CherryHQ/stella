package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ValidateArtifactFiles proves that every declared artifact is a regular file
// inside the repository and verifies an optional SHA-256 digest.
func ValidateArtifactFiles(repositoryRoot string, result Result) error {
	rootInfo, err := os.Lstat(repositoryRoot)
	if err != nil {
		return fmt.Errorf("inspect repository root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("repository root must be a real directory")
	}
	for _, artifact := range result.Artifacts {
		nativePath := filepath.FromSlash(artifact.Path)
		if err := rejectSymlinkComponents(repositoryRoot, nativePath); err != nil {
			return fmt.Errorf("validate artifact %s: %w", artifact.Path, err)
		}
		fullPath := filepath.Join(repositoryRoot, nativePath)
		info, err := os.Lstat(fullPath)
		if err != nil {
			return fmt.Errorf("inspect artifact %s: %w", artifact.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact %s must be a regular file", artifact.Path)
		}
		if artifact.SHA256 == "" {
			continue
		}
		actual, err := fileSHA256(fullPath)
		if err != nil {
			return fmt.Errorf("hash artifact %s: %w", artifact.Path, err)
		}
		if actual != artifact.SHA256 {
			return fmt.Errorf("artifact %s SHA-256 does not match its result record", artifact.Path)
		}
	}
	return nil
}

func rejectSymlinkComponents(root, relativePath string) error {
	current := root
	for component := range strings.SplitSeq(filepath.Clean(relativePath), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %s is a symbolic link", component)
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
