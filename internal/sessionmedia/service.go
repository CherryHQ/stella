// Package sessionmedia persists immutable, owner-scoped bytes for session
// history. An owner is the session principal: the user for a direct session,
// the group for a group session. It owns DB deduplication and deliberately
// receives only asset's narrow media facet, never a mutable path or blob.Store.
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
	"github.com/jackc/pgx/v5/pgtype"
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

// Owner is the session principal that owns a media object. Its two kinds are
// the only owners the schema admits (ctx_media.user_id XOR group_id).
type Owner = asset.MediaOwner

func UserOwner(id uuid.UUID) Owner  { return asset.UserMediaOwner(id) }
func GroupOwner(id uuid.UUID) Owner { return asset.GroupMediaOwner(id) }

// SessionOwner derives the media owner from a session identity. This is the
// single rule: a group session's media belongs to the group, everything else
// belongs to the user whose ID scopes the session. A guest session carries no
// UUID principal, so it fails here rather than minting user-owned media for an
// unlinked channel identity.
func SessionOwner(userID, groupID string) (Owner, error) {
	if groupID != "" {
		id, err := uuid.Parse(groupID)
		if err != nil {
			return Owner{}, fmt.Errorf("%w: group owner %q", ErrInvalidInput, groupID)
		}
		return GroupOwner(id), nil
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return Owner{}, fmt.Errorf("%w: user owner %q", ErrInvalidInput, userID)
	}
	return UserOwner(id), nil
}

// Input contains immutable facts already validated by image ingestion.
type Input struct {
	Owner    Owner
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
//
// It returns the baseline already stored for the object, which is empty for a
// freshly created row and populated whenever these bytes were described before.
// That is what makes a forwarded image free: the caller skips the VLM entirely.
func (s *mediaStore) Persist(ctx context.Context, in Input) (string, ai.ImageBaseline, error) {
	if err := validateInput(in); err != nil {
		return "", ai.ImageBaseline{}, err
	}
	digest := sha256.Sum256(in.Data)
	if err := s.media.PutSessionMedia(ctx, in.Owner, digest, in.Data); err != nil {
		return "", ai.ImageBaseline{}, fmt.Errorf("persist session media blob: %w", err)
	}

	userID, groupID := ownerColumns(in.Owner)
	created, err := s.q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		UserID:    userID,
		GroupID:   groupID,
		Sha256:    digest[:],
		MimeType:  in.MimeType,
		SizeBytes: int64(len(in.Data)),
	})
	if err == nil {
		return created.ID, ai.ImageBaseline{}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", ai.ImageBaseline{}, fmt.Errorf("create session media metadata: %w", err)
	}

	existing, err := s.q.GetMediaByOwnerAndSHA256(ctx, sqlc.GetMediaByOwnerAndSHA256Params{
		OwnerKind: pgtype.Text{String: string(in.Owner.Kind), Valid: true},
		OwnerID:   pgtype.Text{String: in.Owner.ID.String(), Valid: true},
		Sha256:    digest[:],
	})
	if err != nil {
		return "", ai.ImageBaseline{}, fmt.Errorf("get existing session media metadata: %w", err)
	}
	if existing.MimeType != in.MimeType || existing.SizeBytes != int64(len(in.Data)) {
		return "", ai.ImageBaseline{}, fmt.Errorf("%w for sha256 %x", ErrMetadataMismatch, digest)
	}
	return existing.ID, storedBaseline(existing.Baseline), nil
}

// Baselines reads the descriptions already stored for these media objects, so a
// reader can skip both the blob read and the VLM call for anything an earlier
// reader has already described. Unknown and foreign IDs are simply absent.
func (s *mediaStore) Baselines(ctx context.Context, owner Owner, mediaIDs []string) (map[string]ai.ImageBaseline, error) {
	if !owner.Valid() || len(mediaIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListMediaByIDsForOwner(ctx, sqlc.ListMediaByIDsForOwnerParams{
		OwnerKind: pgtype.Text{String: string(owner.Kind), Valid: true},
		OwnerID:   pgtype.Text{String: owner.ID.String(), Valid: true},
		MediaIds:  mediaIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list session media baselines: %w", err)
	}
	out := make(map[string]ai.ImageBaseline, len(rows))
	for _, row := range rows {
		if baseline := storedBaseline(row.Baseline); baseline.Text != "" {
			out[row.ID] = baseline
		}
	}
	return out, nil
}

// StoreBaseline records the first successful render of a media object and
// returns the baseline that is now stored. A second describer of the same bytes
// loses the race and adopts the winner's text: both descriptions are valid, and
// picking one keeps the description of one image stable across every message
// that references it.
func (s *mediaStore) StoreBaseline(ctx context.Context, owner Owner, mediaID string, baseline ai.ImageBaseline) (ai.ImageBaseline, error) {
	// Validate treats the empty baseline as "no baseline", which is legal in a
	// block but not something to store, so it is rejected separately.
	if !owner.Valid() || strings.TrimSpace(mediaID) == "" || baseline.Text == "" {
		return ai.ImageBaseline{}, ErrInvalidInput
	}
	// The column is write-once, so an invalid baseline is not merely useless: the
	// reader would reject it and no later render could ever replace it. Refuse it
	// here rather than let it become permanent.
	if err := baseline.Validate(); err != nil {
		return ai.ImageBaseline{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	ownerKind := pgtype.Text{String: string(owner.Kind), Valid: true}
	ownerID := pgtype.Text{String: owner.ID.String(), Valid: true}
	affected, err := s.q.SetMediaBaselineIfAbsent(ctx, sqlc.SetMediaBaselineIfAbsentParams{
		Baseline:  pgtype.Text{String: baseline.Text, Valid: true},
		ID:        mediaID,
		OwnerKind: ownerKind,
		OwnerID:   ownerID,
	})
	if err != nil {
		return ai.ImageBaseline{}, fmt.Errorf("store session media baseline: %w", err)
	}
	if affected > 0 {
		return baseline, nil
	}
	// Zero rows means another reader described these bytes first, or the row is
	// not this owner's. Re-read: whatever is stored is what every other message
	// referencing this media already shows.
	stored, err := s.Baselines(ctx, owner, []string{mediaID})
	if err != nil {
		return ai.ImageBaseline{}, err
	}
	if existing, ok := stored[mediaID]; ok {
		return existing, nil
	}
	return ai.ImageBaseline{}, nil
}

// storedBaseline turns the nullable column into the domain value. NULL means
// "never rendered successfully"; the schema forbids the empty string, so the
// zero value is unambiguous.
func storedBaseline(column pgtype.Text) ai.ImageBaseline {
	if !column.Valid {
		return ai.ImageBaseline{}
	}
	baseline := ai.ImageBaseline{Text: column.String}
	if baseline.Validate() != nil {
		return ai.ImageBaseline{}
	}
	return baseline
}

// PurgeOwner removes the owner's whole media tree. The database rows are gone
// by the time this runs, so the only thing left to reclaim is storage.
func (s *mediaStore) PurgeOwner(ctx context.Context, owner Owner) error {
	if !owner.Valid() {
		return ErrInvalidInput
	}
	return s.media.DeleteSessionMediaOwner(ctx, owner)
}

// Load verifies that mediaID belongs to owner, then opens its immutable blob.
// Missing, foreign, malformed, and corrupt objects intentionally share the
// same opaque error so a provider request cannot probe another owner's media.
func (s *mediaStore) Load(ctx context.Context, owner Owner, mediaID string) (ai.ImageContent, error) {
	if !owner.Valid() || strings.TrimSpace(mediaID) == "" {
		return ai.ImageContent{}, ErrNotFound
	}
	rows, err := s.q.ListMediaByIDsForOwner(ctx, sqlc.ListMediaByIDsForOwnerParams{
		OwnerKind: pgtype.Text{String: string(owner.Kind), Valid: true},
		OwnerID:   pgtype.Text{String: owner.ID.String(), Valid: true},
		MediaIds:  []string{mediaID},
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
	data, err := s.media.OpenSessionMedia(ctx, owner, digest, row.SizeBytes)
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
	if !in.Owner.Valid() || len(in.Data) == 0 || strings.TrimSpace(in.MimeType) == "" {
		return fmt.Errorf("session media: %w", ErrInvalidInput)
	}
	return nil
}

// ownerColumns projects one owner onto the exactly-one-non-null pair the
// ctx_media check constraint requires.
func ownerColumns(owner Owner) (userID, groupID pgtype.Text) {
	id := pgtype.Text{String: owner.ID.String(), Valid: true}
	if owner.Kind == asset.OwnerGroup {
		return pgtype.Text{}, id
	}
	return id, pgtype.Text{}
}
