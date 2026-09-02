package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/minio/minio-go/v7"

	"github.com/CherryHQ/stella/internal/platform/blob"
)

type S3RawStore struct {
	client    *minio.Client
	bucket    string
	tempDir   string
	admission func(context.Context) error
}

// SupportsOrphanCollection remains false until S3 keys carry a deployment
// namespace or another ownership marker. Exact-key tombstone cleanup is still
// safe and continues to use Delete directly.
func (*S3RawStore) SupportsOrphanCollection() bool { return false }

func NewS3RawStore(
	config blob.S3Config,
	tempDir string,
	admission func(context.Context) error,
) (*S3RawStore, error) {
	client, err := blob.NewS3Client(config)
	if err != nil {
		return nil, err
	}
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	return &S3RawStore{
		client: client, bucket: config.Bucket, tempDir: tempDir, admission: admission,
	}, nil
}

func (s *S3RawStore) Create(ctx context.Context, key string, reader io.Reader) error {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return err
	}
	if reader == nil {
		return fmt.Errorf("library raw reader is required")
	}
	publicationReader, size, cleanup, err := s.prepareReader(ctx, reader)
	if err != nil {
		return err
	}
	defer cleanup()
	// S3 cannot expose trustworthy free-space data. Admission is a deployment
	// circuit-breaker decision based on write errors and observed backlog.
	if s.admission != nil {
		if err := s.admission(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrRawStorageDegraded, err)
		}
	}
	options := minio.PutObjectOptions{DisableMultipart: true, ContentType: "application/octet-stream"}
	options.SetMatchETagExcept("*")
	if _, err := s.client.PutObject(ctx, s.bucket, clean, publicationReader, size, options); err != nil {
		response := minio.ToErrorResponse(err)
		if response.StatusCode == 412 || response.Code == "PreconditionFailed" {
			return ErrRawAlreadyExists
		}
		return fmt.Errorf("publish S3 library raw object: %w", err)
	}
	return nil
}

func (s *S3RawStore) prepareReader(
	ctx context.Context,
	reader io.Reader,
) (io.Reader, int64, func(), error) {
	// CreateManagedUpload already provides a server-only spool. Reusing a
	// seekable reader avoids duplicating those bytes while still letting the S3
	// adapter materialize arbitrary future streams when their size is unknown.
	if seeker, ok := reader.(io.Seeker); ok {
		current, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("locate S3 raw reader: %w", err)
		}
		end, err := seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("size S3 raw reader: %w", err)
		}
		if _, err := seeker.Seek(current, io.SeekStart); err != nil {
			return nil, 0, nil, fmt.Errorf("restore S3 raw reader: %w", err)
		}
		if end < current {
			return nil, 0, nil, fmt.Errorf("S3 raw reader position exceeds its size")
		}
		size := end - current
		if size > MaxFileBytes {
			return nil, 0, nil, fmt.Errorf("%w: maximum is %d bytes", ErrFileTooLarge, MaxFileBytes)
		}
		return contextReader(ctx, reader), size, func() {}, nil
	}

	temp, err := os.CreateTemp(s.tempDir, ".stella-library-s3-raw-*")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("create S3 raw spool: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("restrict S3 raw spool: %w", err)
	}
	size, err := copyBounded(ctx, temp, reader, MaxFileBytes)
	if err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("sync S3 raw spool: %w", err)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("rewind S3 raw spool: %w", err)
	}
	return contextReader(ctx, temp), size, cleanup, nil
}

func (s *S3RawStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return nil, err
	}
	return s.client.GetObject(ctx, s.bucket, clean, minio.GetObjectOptions{})
}

func (s *S3RawStore) Delete(ctx context.Context, key string) error {
	clean, err := blob.ValidateKey(key)
	if err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, clean, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete S3 library raw object: %w", err)
	}
	return nil
}

func (s *S3RawStore) ListPage(
	ctx context.Context,
	prefix, cursor string,
	limit int,
) (RawPage, error) {
	cleanPrefix, err := validateRawListRequest(prefix, cursor, limit)
	if err != nil {
		return RawPage{}, err
	}
	prefixWithSlash := cleanPrefix + "/"
	cleanCursor, err := validateRawListCursor(cleanPrefix, cursor)
	if err != nil {
		return RawPage{}, err
	}

	listContext, cancel := context.WithCancel(ctx)
	defer cancel()
	objects := make([]RawObject, 0, limit+1)
	for object := range s.client.ListObjects(listContext, s.bucket, minio.ListObjectsOptions{
		Prefix: prefixWithSlash, Recursive: true, StartAfter: cleanCursor, MaxKeys: limit + 1,
	}) {
		if object.Err != nil {
			if len(objects) > limit && errors.Is(object.Err, context.Canceled) {
				continue
			}
			return RawPage{}, fmt.Errorf("list S3 library raw objects: %w", object.Err)
		}
		objects = append(objects, RawObject{
			Key: object.Key, Size: object.Size, LastModified: object.LastModified.UTC(),
		})
		if len(objects) == limit+1 {
			cancel()
		}
	}
	if len(objects) <= limit {
		return RawPage{Objects: objects}, nil
	}
	return RawPage{
		Objects:    objects[:limit],
		NextCursor: objects[limit-1].Key,
	}, nil
}
