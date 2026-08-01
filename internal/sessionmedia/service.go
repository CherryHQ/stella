// Package sessionmedia persists immutable, user-owned bytes for ordinary
// session history. It owns DB deduplication and deliberately receives only
// asset's narrow media facet, never a mutable path or blob.Store.
package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrInvalidInput     = errors.New("invalid session media input")
	ErrMetadataMismatch = errors.New("session media metadata mismatch")
	ErrNotFound         = errors.New("session media not found")
)

// Input contains immutable facts already validated by image ingestion.
type Input struct {
	UserID   uuid.UUID
	Data     []byte
	MimeType string
}

// mediaStore stores one content-addressed object before creating or reusing its
// metadata row. Blob-first/DB-second leaves only safe unreachable objects if a
// DB write fails; it never commits a dangling media reference.
type mediaStore struct {
	media asset.SessionMediaStore
	q     *sqlc.Queries
}

func newMediaStore(media asset.SessionMediaStore, db *pgxpool.Pool) (*mediaStore, error) {
	if media == nil || db == nil {
		return nil, fmt.Errorf("session media service: %w", ErrInvalidInput)
	}
	return &mediaStore{media: media, q: sqlc.New(db)}, nil
}

// Persist writes the verified content-addressed object before inserting metadata.
// Existing rows may only be reused when every immutable metadata field matches.
func (s *mediaStore) Persist(ctx context.Context, in Input) (string, error) {
	if err := validateInput(in); err != nil {
		return "", err
	}
	digest := sha256.Sum256(in.Data)
	if err := s.media.PutSessionMedia(ctx, in.UserID, digest, in.Data); err != nil {
		return "", fmt.Errorf("persist session media blob: %w", err)
	}

	created, err := s.q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		UserID:    in.UserID.String(),
		Sha256:    digest[:],
		MimeType:  in.MimeType,
		SizeBytes: int64(len(in.Data)),
	})
	if err == nil {
		return created.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("create session media metadata: %w", err)
	}

	existing, err := s.q.GetMediaByUserAndSHA256(ctx, sqlc.GetMediaByUserAndSHA256Params{
		UserID: in.UserID.String(),
		Sha256: digest[:],
	})
	if err != nil {
		return "", fmt.Errorf("get existing session media metadata: %w", err)
	}
	if existing.MimeType != in.MimeType || existing.SizeBytes != int64(len(in.Data)) {
		return "", fmt.Errorf("%w for sha256 %x", ErrMetadataMismatch, digest)
	}
	return existing.ID, nil
}

// Load verifies that mediaID belongs to userID, then opens its immutable blob.
// Missing, foreign, malformed, and corrupt objects intentionally share the
// same opaque error so a provider request cannot probe another user's media.
func (s *mediaStore) Load(ctx context.Context, userID uuid.UUID, mediaID string) (ai.ImageContent, error) {
	if userID == uuid.Nil || strings.TrimSpace(mediaID) == "" {
		return ai.ImageContent{}, ErrNotFound
	}
	rows, err := s.q.ListMediaByIDsForUser(ctx, sqlc.ListMediaByIDsForUserParams{
		UserID:   userID.String(),
		MediaIds: []string{mediaID},
	})
	if err != nil || len(rows) != 1 {
		return ai.ImageContent{}, ErrNotFound
	}
	row := rows[0]
	if len(row.Sha256) != sha256.Size || row.SizeBytes <= 0 || strings.TrimSpace(row.MimeType) == "" {
		return ai.ImageContent{}, ErrNotFound
	}
	var digest [sha256.Size]byte
	copy(digest[:], row.Sha256)
	data, err := s.media.OpenSessionMedia(ctx, userID, digest, row.SizeBytes)
	if err != nil {
		return ai.ImageContent{}, ErrNotFound
	}
	cfg, mime, err := vision.ValidateImage(data, row.MimeType)
	if err != nil {
		return ai.ImageContent{}, ErrNotFound
	}
	prepared, preparedMIME, err := vision.PrepareRendererPayloadContext(ctx, data, cfg, mime)
	if err != nil {
		return ai.ImageContent{}, ErrNotFound
	}
	return ai.ImageContent{Data: base64.StdEncoding.EncodeToString(prepared), MimeType: preparedMIME}, nil
}

func validateInput(in Input) error {
	if in.UserID == uuid.Nil || len(in.Data) == 0 || strings.TrimSpace(in.MimeType) == "" {
		return fmt.Errorf("session media: %w", ErrInvalidInput)
	}
	return nil
}
