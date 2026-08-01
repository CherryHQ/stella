// Package sessionmedia persists immutable, user-owned bytes for ordinary
// session history. It owns DB deduplication and deliberately receives only
// asset's narrow media facet, never a mutable path or blob.Store.
package sessionmedia

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrInvalidInput     = errors.New("invalid session media input")
	ErrMetadataMismatch = errors.New("session media metadata mismatch")
)

// Input contains immutable facts already established by the image-ingestion
// boundary. Phase 1 deliberately does not decode or enrich images; later phases
// validate MIME and dimensions before calling Persist.
type Input struct {
	UserID   uuid.UUID
	Data     []byte
	MimeType string
	WidthPX  int32
	HeightPX int32
}

// Service stores one content-addressed object before creating or reusing its
// metadata row. Blob-first/DB-second leaves only safe unreachable objects if a
// DB write fails; it never commits a dangling media reference.
type Service struct {
	media asset.SessionMediaStore
	q     *sqlc.Queries
}

func New(media asset.SessionMediaStore, q *sqlc.Queries) (*Service, error) {
	if media == nil || q == nil {
		return nil, fmt.Errorf("session media service: %w", ErrInvalidInput)
	}
	return &Service{media: media, q: q}, nil
}

// Persist writes the verified content-addressed object before inserting metadata.
// Existing rows may only be reused when every immutable metadata field matches.
func (s *Service) Persist(ctx context.Context, in Input) (sqlc.CtxMedium, error) {
	if err := validateInput(in); err != nil {
		return sqlc.CtxMedium{}, err
	}
	digest := sha256.Sum256(in.Data)
	if err := s.media.PutSessionMedia(ctx, in.UserID, digest, in.Data); err != nil {
		return sqlc.CtxMedium{}, fmt.Errorf("persist session media blob: %w", err)
	}

	created, err := s.q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		UserID:    in.UserID.String(),
		Sha256:    digest[:],
		MimeType:  in.MimeType,
		SizeBytes: int64(len(in.Data)),
		WidthPx:   in.WidthPX,
		HeightPx:  in.HeightPX,
	})
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.CtxMedium{}, fmt.Errorf("create session media metadata: %w", err)
	}

	existing, err := s.q.GetMediaByUserAndSHA256(ctx, sqlc.GetMediaByUserAndSHA256Params{
		UserID: in.UserID.String(),
		Sha256: digest[:],
	})
	if err != nil {
		return sqlc.CtxMedium{}, fmt.Errorf("get existing session media metadata: %w", err)
	}
	if existing.MimeType != in.MimeType ||
		existing.SizeBytes != int64(len(in.Data)) ||
		existing.WidthPx != in.WidthPX ||
		existing.HeightPx != in.HeightPX {
		return sqlc.CtxMedium{}, fmt.Errorf("%w for sha256 %x", ErrMetadataMismatch, digest)
	}
	return existing, nil
}

func validateInput(in Input) error {
	if in.UserID == uuid.Nil || len(in.Data) == 0 || strings.TrimSpace(in.MimeType) == "" || in.WidthPX <= 0 || in.HeightPX <= 0 {
		return fmt.Errorf("session media: %w", ErrInvalidInput)
	}
	return nil
}
