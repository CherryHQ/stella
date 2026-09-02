package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const SnapshotID = "models.dev"

type SnapshotRecord struct {
	Payload  []byte
	ETag     string
	SyncedAt time.Time
}

type SnapshotStore interface {
	GetModelCatalog(context.Context) (SnapshotRecord, error)
}

// Load uses a valid database snapshot when available and falls back to the
// embedded copy for offline startup or a damaged upstream payload.
func Load(ctx context.Context, store SnapshotStore, log *slog.Logger) (*Catalog, SnapshotRecord, error) {
	if store != nil {
		record, err := store.GetModelCatalog(ctx)
		if err == nil && len(record.Payload) > 0 {
			catalog, decodeErr := decode(record.Payload)
			if decodeErr == nil {
				return catalog, record, nil
			}
			if log != nil {
				log.Warn("failed to decode database model catalog; using embedded snapshot", "error", decodeErr)
			}
		}
	}
	catalog, err := Embedded()
	if err != nil {
		return nil, SnapshotRecord{}, fmt.Errorf("load embedded model catalog: %w", err)
	}
	return catalog, SnapshotRecord{}, nil
}
