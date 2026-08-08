package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
)

const (
	RawPrefix          = "library/files"
	MaxRawListPageSize = 500
	// DefaultFSMinFreeBytes keeps a local deployment from consuming the final
	// disk reserve with immutable library snapshots.
	DefaultFSMinFreeBytes int64 = 5 << 30
)

var (
	ErrRawAlreadyExists    = errors.New("library raw object already exists")
	ErrRawStorageDegraded  = errors.New("library raw storage is degraded")
	ErrInvalidRawStorePage = errors.New("invalid library raw store page")
)

// RawObject is the minimum storage metadata required by bounded orphan GC.
type RawObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// RawPage is one bounded canonical-key-ordered enumeration page. NextCursor is
// the last returned key and is an exclusive lower bound for the following page.
type RawPage struct {
	Objects    []RawObject
	NextCursor string
}

// RawStore owns canonical immutable raw snapshots. Create must never replace an
// existing key. ListPage must order by canonical key and bound both returned
// objects and adapter memory; it does not provide snapshot isolation across
// concurrent pages.
type RawStore interface {
	Create(ctx context.Context, key string, reader io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	ListPage(ctx context.Context, prefix, cursor string, limit int) (RawPage, error)
	// SupportsOrphanCollection is true only when RawPrefix is owned exclusively
	// by this Stella deployment, so unknown objects can be deleted safely.
	SupportsOrphanCollection() bool
}

// RawStoreOptions holds backend admission knobs without exposing a second
// Library-specific storage configuration group.
type RawStoreOptions struct {
	TempDir        string
	FSMinFreeBytes int64
	S3Admission    func(context.Context) error
}

// NewRawStoreFromConfig selects local FS or deployment S3 using the existing
// STELLA_BLOB_S3_* configuration group.
func NewRawStoreFromConfig(
	root string,
	deploymentS3 config.BlobS3Config,
	options RawStoreOptions,
) (RawStore, error) {
	s3Config, err := blob.ResolveS3Config(deploymentS3)
	if err != nil {
		return nil, err
	}
	if s3Config != nil {
		return NewS3RawStore(*s3Config, options.TempDir, options.S3Admission)
	}
	return NewFSRawStore(root, options.FSMinFreeBytes)
}

// RawKey derives the only canonical object key for a LibraryFile.
func RawKey(fileID string) (string, error) {
	parsed, err := uuid.Parse(fileID)
	if err != nil {
		return "", fmt.Errorf("invalid library file ID: %w", err)
	}
	if parsed.String() != fileID {
		return "", fmt.Errorf("library file ID %q is not canonical", fileID)
	}
	return path.Join(RawPrefix, fileID, "source"), nil
}

// FileIDFromRawKey validates a canonical key before reconciliation uses it as
// a database identity. Malformed objects are retained, never guessed at.
func FileIDFromRawKey(key string) (string, error) {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return "", err
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 4 || parts[0] != "library" || parts[1] != "files" || parts[3] != "source" {
		return "", fmt.Errorf("invalid library raw key %q", key)
	}
	parsed, err := uuid.Parse(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid library raw file ID: %w", err)
	}
	if parsed.String() != parts[2] {
		return "", fmt.Errorf("library raw file ID %q is not canonical", parts[2])
	}
	return parts[2], nil
}

func validateRawListRequest(prefix, cursor string, limit int) (string, error) {
	clean, err := blob.ValidateKey(prefix)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRawStorePage, err)
	}
	if limit < 1 || limit > MaxRawListPageSize {
		return "", fmt.Errorf(
			"%w: limit must be between 1 and %d",
			ErrInvalidRawStorePage,
			MaxRawListPageSize,
		)
	}
	if cursor != "" && strings.ContainsRune(cursor, '\x00') {
		return "", fmt.Errorf("%w: cursor contains NUL", ErrInvalidRawStorePage)
	}
	return clean, nil
}

func validateRawListCursor(prefix, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	clean, err := blob.ValidateKey(cursor)
	if err != nil || !strings.HasPrefix(clean, prefix+"/") {
		return "", fmt.Errorf("%w: cursor is outside prefix", ErrInvalidRawStorePage)
	}
	return clean, nil
}

func contextReader(ctx context.Context, reader io.Reader) io.Reader {
	return readerFunc(func(buffer []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return reader.Read(buffer)
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(buffer []byte) (int, error) { return f(buffer) }
