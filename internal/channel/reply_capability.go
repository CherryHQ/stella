package channel

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DurableReplyCapability is a decrypted, expiring egress credential. It is
// returned only at the publish boundary and is never copied into an outbox
// envelope or log field.
type DurableReplyCapability struct {
	Secret    string
	ExpiresAt time.Time
}

// DurableReplyCapabilityResolver resolves an opaque outbox capability ID while
// enforcing channel ownership, kind, and expiry in PostgreSQL.
type DurableReplyCapabilityResolver interface {
	Resolve(context.Context, string, string, string) (DurableReplyCapability, error)
}

type durableReplyCapabilityResolver struct {
	q     *sqlc.Queries
	vault *vault.Service
}

func NewDurableReplyCapabilityResolver(db *pgxpool.Pool, vaultSvc *vault.Service) DurableReplyCapabilityResolver {
	if db == nil || vaultSvc == nil {
		return nil
	}
	return &durableReplyCapabilityResolver{q: sqlc.New(db), vault: vaultSvc}
}

func (r *durableReplyCapabilityResolver) Resolve(ctx context.Context, id, channelID, kind string) (DurableReplyCapability, error) {
	row, err := r.q.GetLiveChannelReplyCapability(ctx, sqlc.GetLiveChannelReplyCapabilityParams{ID: id, ChannelID: channelID})
	if err != nil {
		return DurableReplyCapability{}, err
	}
	if row.Kind != kind {
		return DurableReplyCapability{}, fmt.Errorf("reply capability has unexpected kind %q", row.Kind)
	}
	secret, err := r.vault.DecryptSystem(row.Ciphertext)
	if err != nil {
		return DurableReplyCapability{}, fmt.Errorf("decrypt reply capability: %w", err)
	}
	return DurableReplyCapability{Secret: secret, ExpiresAt: row.ExpiresAt}, nil
}
