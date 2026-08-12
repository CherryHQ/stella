package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var ErrGuestLimitReached = errors.New("channel guest limit reached")

// GuestStore resolves the durable principal for an unlinked channel identity.
type GuestStore interface {
	ResolveOrCreateGuest(ctx context.Context, channelID, platform, externalID string, maxGuests int) (sqlc.ChannelGuest, error)
}

// GuestDB is the sqlc subset used by the guest store.
type GuestDB interface {
	CreateChannelGuest(context.Context, sqlc.CreateChannelGuestParams) (sqlc.ChannelGuest, error)
	UpdateChannelGuestActivityByExternalID(context.Context, sqlc.UpdateChannelGuestActivityByExternalIDParams) (sqlc.ChannelGuest, error)
}

type guestStore struct{ q GuestDB }

// NewGuestStore creates a PostgreSQL-backed GuestStore.
func NewGuestStore(db sqlc.DBTX) GuestStore { return &guestStore{q: sqlc.New(db)} }

// GuestRetention owns expiry of inactive channel guests and their cascading
// session data.
type GuestRetention interface {
	PurgeExpired(context.Context) (int64, error)
}

type guestRetention struct{ q *sqlc.Queries }

// NewGuestRetention creates a PostgreSQL-backed guest retention service.
func NewGuestRetention(db sqlc.DBTX) GuestRetention {
	return &guestRetention{q: sqlc.New(db)}
}

func (r *guestRetention) PurgeExpired(ctx context.Context) (int64, error) {
	deleted, err := r.q.PurgeExpiredChannelGuest(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge expired channel guests: %w", err)
	}
	return deleted, nil
}

func (s *guestStore) ResolveOrCreateGuest(ctx context.Context, channelID, platform, externalID string, maxGuests int) (sqlc.ChannelGuest, error) {
	key := sqlc.UpdateChannelGuestActivityByExternalIDParams{ChannelID: channelID, Platform: platform, ExternalID: externalID}
	guest, err := s.q.UpdateChannelGuestActivityByExternalID(ctx, key)
	if err == nil {
		return guest, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelGuest{}, fmt.Errorf("get channel guest: %w", err)
	}
	guest, err = s.q.CreateChannelGuest(ctx, sqlc.CreateChannelGuestParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: channelID, Platform: platform, ExternalID: externalID, MaxGuests: int64(maxGuests),
	})
	if err == nil {
		return guest, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_guest_channel_id_platform_external_id_key" {
		guest, readErr := s.q.UpdateChannelGuestActivityByExternalID(ctx, key)
		if readErr != nil {
			return sqlc.ChannelGuest{}, fmt.Errorf("read channel guest after create race: %w", readErr)
		}
		return guest, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelGuest{}, ErrGuestLimitReached
	}
	return sqlc.ChannelGuest{}, fmt.Errorf("create channel guest: %w", err)
}
