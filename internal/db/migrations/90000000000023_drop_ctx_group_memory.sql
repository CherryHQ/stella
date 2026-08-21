-- +goose Up
DROP TABLE ctx_group_memory;

-- +goose Down
CREATE TABLE ctx_group_memory (
    group_id uuid PRIMARY KEY REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    content text NOT NULL DEFAULT '',
    version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
