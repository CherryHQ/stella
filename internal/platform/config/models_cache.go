package config

import "time"

// CachedModel is one fetched provider/model pair. The set of fetched models per
// provider is persisted in PostgreSQL (see Store.ListCachedModels) so it is
// shared across replicas and survives restarts; ProviderName is filled by readers
// from the live provider config, not stored with the cache.
type CachedModel struct {
	Provider     string    `json:"provider"`
	ProviderName string    `json:"provider_name,omitempty"`
	Model        string    `json:"model"`
	SyncedAt     time.Time `json:"synced_at,omitempty"`
}
