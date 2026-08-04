-- +goose Up
-- agent_provider_credential overrides the API key an Agent uses for one
-- canonical global Provider. All other Provider metadata (type, base URL, model
-- catalog, enabled state) stays on the global provider row; this table carries
-- only the encrypted key. The composite primary key expresses the real fact:
-- one Agent has at most one key override per canonical Provider, while different
-- model tiers may still reference different Providers. api_key_enc is ciphertext
-- produced by the vault system key and is never stored in plaintext; the CHECK
-- guarantees an override row always carries key material (DELETE, not an empty
-- string, restores global-key fallback).
CREATE TABLE agent_provider_credential (
    agent_id    TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES provider(id) ON DELETE CASCADE,
    api_key_enc TEXT NOT NULL CHECK (api_key_enc <> ''),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, provider_id)
);
-- The composite PK already indexes agent_id (its leading column); this index
-- covers the reverse direction so a Provider delete cascade and any
-- provider-scoped lookup avoid a sequential scan.
CREATE INDEX idx_agent_provider_credential_provider_id
    ON agent_provider_credential (provider_id);

-- +goose Down
DROP TABLE agent_provider_credential;
