-- +goose Up
CREATE TABLE recally_article_content (
    article_id  TEXT PRIMARY KEY REFERENCES recally_article(id) ON DELETE CASCADE,
    content     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE recally_article_content;
