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

// GuestStore resolves the durable principal for an unlinked channel identity.
type GuestStore interface {
	ResolveOrCreateGuest(ctx context.Context, channelID, platform, externalID string) (sqlc.ChannelGuest, error)
}

// GuestDB is the sqlc subset used by the guest store.
type GuestDB interface {
	CreateChannelGuest(context.Context, sqlc.CreateChannelGuestParams) (sqlc.ChannelGuest, error)
	GetChannelGuestByExternalID(context.Context, sqlc.GetChannelGuestByExternalIDParams) (sqlc.ChannelGuest, error)
}

type guestStore struct{ q GuestDB }

// NewGuestStore creates a PostgreSQL-backed GuestStore.
func NewGuestStore(q GuestDB) GuestStore { return &guestStore{q: q} }

func (s *guestStore) ResolveOrCreateGuest(ctx context.Context, channelID, platform, externalID string) (sqlc.ChannelGuest, error) {
	key := sqlc.GetChannelGuestByExternalIDParams{ChannelID: channelID, Platform: platform, ExternalID: externalID}
	guest, err := s.q.GetChannelGuestByExternalID(ctx, key)
	if err == nil {
		return guest, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelGuest{}, fmt.Errorf("get channel guest: %w", err)
	}
	guest, err = s.q.CreateChannelGuest(ctx, sqlc.CreateChannelGuestParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: channelID, Platform: platform, ExternalID: externalID,
	})
	if err == nil {
		return guest, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_guest_channel_id_platform_external_id_key" {
		guest, readErr := s.q.GetChannelGuestByExternalID(ctx, key)
		if readErr != nil {
			return sqlc.ChannelGuest{}, fmt.Errorf("read channel guest after create race: %w", readErr)
		}
		return guest, nil
	}
	return sqlc.ChannelGuest{}, fmt.Errorf("create channel guest: %w", err)
}
