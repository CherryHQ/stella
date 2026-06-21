-- Embedding ownership is polymorphic: owner_id points at the table named by
-- owner_kind. Search queries must join through that owner table before returning
-- hits to enforce tenant/user scoping; this table intentionally has no direct
-- user_id because supported owners use different primary-key types.
CREATE TABLE search_embedding (
    id           UUID PRIMARY KEY DEFAULT uuidv7(),
    owner_kind   TEXT NOT NULL,
    owner_id     TEXT NOT NULL,
    model        TEXT NOT NULL,
    dims         BIGINT NOT NULL CHECK (dims > 0),
    content_hash BYTEA NOT NULL,
    embedding    BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_kind, owner_id, model)
);

CREATE INDEX idx_search_embedding_owner ON search_embedding (owner_kind, owner_id);
CREATE INDEX idx_search_embedding_model_updated ON search_embedding (model, updated_at);
