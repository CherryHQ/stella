package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/blob"
)

const fsReadDirBatchSize = 128

type FSRawStore struct {
	root           string
	minFreeBytes   int64
	availableBytes func(string) (int64, error)
}

func NewFSRawStore(root string, minFreeBytes int64) (*FSRawStore, error) {
	if root == "" {
		return nil, fmt.Errorf("knowledge raw FS root is required")
	}
	if minFreeBytes < 0 {
		return nil, fmt.Errorf("knowledge raw FS minimum free bytes cannot be negative")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &FSRawStore{root: abs, minFreeBytes: minFreeBytes, availableBytes: availableDiskBytes}, nil
}

func (s *FSRawStore) Create(ctx context.Context, key string, reader io.Reader) error {
	if reader == nil {
		return fmt.Errorf("knowledge raw reader is required")
	}
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create knowledge raw directory: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(target), ".stella-knowledge-raw-*")
	if err != nil {
		return fmt.Errorf("create knowledge raw temp file: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("restrict knowledge raw temp file: %w", err)
	}

	written, err := copyBounded(ctx, temp, reader, MaxFileBytes)
	if err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync knowledge raw temp file: %w", err)
	}
	// Refresh immediately before publication so LastModified measures the age of
	// the canonical object, not how long an upload spent writing its temp file.
	now := time.Now()
	if err := os.Chtimes(tempName, now, now); err != nil {
		_ = temp.Close()
		return fmt.Errorf("timestamp knowledge raw temp file: %w", err)
	}
	if s.minFreeBytes > 0 {
		available, err := s.availableBytes(filepath.Dir(target))
		if err != nil {
			_ = temp.Close()
			return fmt.Errorf("inspect knowledge raw free space: %w", err)
		}
		// The temp file already consumes written bytes, so the current free-space
		// figure is the post-write value that must remain above the low-water mark.
		if available < s.minFreeBytes {
			_ = temp.Close()
			return fmt.Errorf(
				"%w: available=%d required=%d written=%d",
				ErrRawStorageDegraded,
				available,
				s.minFreeBytes,
				written,
			)
		}
	}
	if err := ctx.Err(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close knowledge raw temp file: %w", err)
	}

	// A same-directory hard link is an atomic no-replace publication primitive:
	// unlike Rename it cannot overwrite an existing canonical snapshot.
	if err := os.Link(tempName, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrRawAlreadyExists
		}
		return fmt.Errorf("publish knowledge raw object: %w", err)
	}
	return nil
}

func (s *FSRawStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectPath, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(objectPath)
}

func (s *FSRawStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	objectPath, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(objectPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete knowledge raw object: %w", err)
	}
	return nil
}

func (s *FSRawStore) ListPage(
	ctx context.Context,
	prefix, cursor string,
	limit int,
) (RawPage, error) {
	cleanPrefix, err := validateRawListRequest(prefix, cursor, limit)
	if err != nil {
		return RawPage{}, err
	}
	offset, err := decodeFSCursor(cursor)
	if err != nil {
		return RawPage{}, err
	}
	directory, err := s.path(cleanPrefix)
	if err != nil {
		return RawPage{}, err
	}
	if _, err := os.Stat(directory); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RawPage{}, nil
		}
		return RawPage{}, fmt.Errorf("inspect knowledge raw prefix: %w", err)
	}

	objects := make([]RawObject, 0, limit+1)
	var seen int64
	stop := errors.New("raw page complete")
	err = walkFilesBounded(ctx, directory, func(objectPath string, info fs.FileInfo) error {
		if seen < offset {
			seen++
			return nil
		}
		relative, err := filepath.Rel(s.root, objectPath)
		if err != nil {
			return err
		}
		objects = append(objects, RawObject{
			Key:          filepath.ToSlash(relative),
			Size:         info.Size(),
			LastModified: info.ModTime().UTC(),
		})
		seen++
		if len(objects) == limit+1 {
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return RawPage{}, err
	}
	if len(objects) <= limit {
		return RawPage{Objects: objects}, nil
	}
	return RawPage{
		Objects:    objects[:limit],
		NextCursor: encodeFSCursor(offset + int64(limit)),
	}, nil
}

func (s *FSRawStore) path(key string) (string, error) {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return "", err
	}
	objectPath := filepath.Join(s.root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(s.root, objectPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("knowledge raw key escapes root: %q", key)
	}
	return objectPath, nil
}

func copyBounded(ctx context.Context, writer io.Writer, reader io.Reader, limit int64) (int64, error) {
	written, err := io.CopyBuffer(
		writer,
		io.LimitReader(contextReader(ctx, reader), limit+1),
		make([]byte, spoolCopyBufferSize),
	)
	if err != nil {
		return written, fmt.Errorf("write knowledge raw object: %w", err)
	}
	if written > limit {
		return written, fmt.Errorf("%w: maximum is %d bytes", ErrFileTooLarge, limit)
	}
	return written, nil
}

// walkFilesBounded uses fixed-size ReadDir batches rather than WalkDir, whose
// sorted directory reads can load every object name under one prefix at once.
func walkFilesBounded(ctx context.Context, root string, visit func(string, fs.FileInfo) error) error {
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(fsReadDirBatchSize)
		for _, entry := range entries {
			entryPath := filepath.Join(root, entry.Name())
			if entry.IsDir() {
				if err := walkFilesBounded(ctx, entryPath, visit); err != nil {
					return err
				}
				continue
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				if err := visit(entryPath, info); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func encodeFSCursor(offset int64) string { return "fs:" + strconv.FormatInt(offset, 10) }

func decodeFSCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, ok := strings.CutPrefix(cursor, "fs:")
	if !ok {
		return 0, fmt.Errorf("%w: invalid FS cursor", ErrInvalidRawStorePage)
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: invalid FS cursor", ErrInvalidRawStorePage)
	}
	return offset, nil
}
