package agentpackage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxPublishedFileBytes  = 32 << 20
	maxPublishedTotalBytes = 128 << 20
	maxPublishedEntries    = 4096
)

// PublishedPackage is an immutable, content-addressed package directory.
// The returned Package was parsed from the published directory, never from
// the caller's mutable source tree.
type PublishedPackage struct {
	Digest  string
	Root    string
	Package *Package
}

// PublishDirectory copies and validates a package before exposing it at its
// digest path. Symlinks and special files are rejected, bytes are bounded and
// fsynced, and the final directory is published with one rename. A caller can
// safely CAS a Definition.Spec only after this function returns.
func PublishDirectory(source, destinationRoot string) (PublishedPackage, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(destinationRoot) == "" {
		return PublishedPackage{}, errors.New("agentpackage: source and destination are required")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return PublishedPackage{}, fmt.Errorf("stat package source: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return PublishedPackage{}, errors.New("agentpackage: package source must be a directory")
	}
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		return PublishedPackage{}, fmt.Errorf("create package store: %w", err)
	}
	staging, err := os.MkdirTemp(destinationRoot, ".staging-")
	if err != nil {
		return PublishedPackage{}, fmt.Errorf("create package staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := copyPackageTree(source, staging); err != nil {
		return PublishedPackage{}, err
	}
	pkg, diagnostics := load(staging, true)
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return PublishedPackage{}, fmt.Errorf("validate staged package: %s", diagnostic.Message)
		}
	}
	if pkg == nil || diagnostics.HasErrors() {
		return PublishedPackage{}, fmt.Errorf("load staged package: invalid package")
	}
	digest, err := DirectoryDigest(staging)
	if err != nil {
		return PublishedPackage{}, err
	}
	final := filepath.Join(destinationRoot, strings.TrimPrefix(digest, "sha256:"))
	if existing, err := os.Stat(final); err == nil {
		if !existing.IsDir() {
			return PublishedPackage{}, fmt.Errorf("package digest path is not a directory: %s", final)
		}
		existingDigest, err := DirectoryDigest(final)
		if err != nil || existingDigest != digest {
			return PublishedPackage{}, fmt.Errorf("existing package digest does not match: %s", final)
		}
		loaded, loadedDiagnostics := Load(final)
		if loaded == nil || loadedDiagnostics.HasErrors() {
			return PublishedPackage{}, fmt.Errorf("existing package digest path is invalid: %s", final)
		}
		_ = os.RemoveAll(staging)
		return PublishedPackage{Digest: digest, Root: final, Package: loaded}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return PublishedPackage{}, fmt.Errorf("stat package digest path: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		// Another publisher may have won this digest while we copied.
		if existingDigest, verifyErr := DirectoryDigest(final); verifyErr != nil || existingDigest != digest {
			return PublishedPackage{}, fmt.Errorf("publish package: %w", err)
		}
	}
	if err := syncDirectory(destinationRoot); err != nil {
		return PublishedPackage{}, err
	}
	pkg, diagnostics = Load(final)
	if pkg == nil || diagnostics.HasErrors() {
		return PublishedPackage{}, errors.New("agentpackage: published package failed final verification")
	}
	return PublishedPackage{Digest: digest, Root: final, Package: pkg}, nil
}

func copyPackageTree(source, destination string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var total int64
	entries := 0
	directories := []string{destination}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		entries++
		if entries > maxPublishedEntries {
			return errors.New("agentpackage: package exceeds entry limit")
		}
		rel := filepath.FromSlash(path)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("agentpackage: symlink is not allowed: %s", filepath.ToSlash(rel))
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			directories = append(directories, target)
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("agentpackage: special file is not allowed: %s", filepath.ToSlash(rel))
		}
		if info.Size() > maxPublishedFileBytes {
			return fmt.Errorf("agentpackage: file %s exceeds size limit", filepath.ToSlash(rel))
		}
		copied, err := copyRegularFile(root, path, target, info, maxPublishedTotalBytes-total)
		if err != nil {
			return fmt.Errorf("copy package file %s: %w", filepath.ToSlash(rel), err)
		}
		total += copied
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy package tree: %w", err)
	}
	// Persist children before their parent directory entries.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(root *os.Root, source, destination string, expected fs.FileInfo, remaining int64) (int64, error) {
	in, err := root.Open(source)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return 0, errors.New("package source changed while copying")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()
	limit := min(int64(maxPublishedFileBytes), remaining)
	copied, err := io.CopyN(out, in, limit+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if copied > limit {
		return 0, errors.New("package content exceeds size limit")
	}
	// File modes participate in the digest; the daemon's umask must not
	// change the identity or executable bits of the authored package.
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	return copied, out.Close()
}

// DirectoryDigest returns the stable digest of every regular file below root.
// It is the asset identity used inside a definition's Content reference.
func DirectoryDigest(root string) (string, error) {
	directory, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	manifestHash := sha256.New()
	var total int64
	entries := 0
	err = fs.WalkDir(directory.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		entries++
		if entries > maxPublishedEntries {
			return errors.New("agentpackage: package exceeds entry limit")
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("agentpackage: published tree contains symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("agentpackage: published tree contains special file")
		}
		file, err := directory.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return errors.New("agentpackage: published file changed during verification")
		}
		hash := sha256.New()
		limit := min(int64(maxPublishedFileBytes), maxPublishedTotalBytes-total)
		size, err := io.CopyN(hash, file, limit+1)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if size > limit {
			return errors.New("agentpackage: package content exceeds size limit")
		}
		total += size
		// WalkDir's lexical order gives the manifest a stable encoding.
		_, _ = fmt.Fprintf(manifestHash, "%s\x00%04o\x00%d\x00%s\n", path, opened.Mode().Perm(), size, hex.EncodeToString(hash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest package tree: %w", err)
	}
	sum := manifestHash.Sum(nil)
	return "sha256:" + hex.EncodeToString(sum), nil
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open package directory for sync: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync package directory: %w", err)
	}
	return nil
}
