CREATE TABLE recally_article (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id      TEXT REFERENCES agent(id) ON DELETE SET NULL,
    url           TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    source_type   TEXT NOT NULL DEFAULT 'web',
    title         TEXT NOT NULL DEFAULT '',
    author        TEXT NOT NULL DEFAULT '',
    summary       TEXT NOT NULL DEFAULT '',
    tags          TEXT NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'unread',
    starred       BOOLEAN NOT NULL DEFAULT false,
    file_path     TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}',
    published_at  TIMESTAMPTZ,
    saved_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Weighted full-text vector. A>B>C reproduces the former fts5
    -- bm25(4.0, 2.0, 2.0, 1.0) ranking: title > summary = tags > author.
    search_tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(summary, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(tags, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(author, '')), 'C')
    ) STORED
);

CREATE UNIQUE INDEX idx_recally_article_user_canonical ON recally_article (user_id, canonical_url);
CREATE INDEX idx_recally_article_user_status ON recally_article (user_id, status);
CREATE INDEX idx_recally_article_user_source ON recally_article (user_id, source_type);
CREATE INDEX idx_recally_article_user_starred ON recally_article (user_id, starred) WHERE starred = true;
CREATE INDEX idx_recally_article_saved_at ON recally_article (saved_at);
CREATE INDEX idx_recally_article_tsv ON recally_article USING GIN (search_tsv);
-- Per-column trigram indexes for the CJK/substring SearchArticlesLike fallback,
-- which ILIKEs title/summary/tags/author.
CREATE INDEX idx_recally_article_title_trgm ON recally_article USING GIN (title gin_trgm_ops);
CREATE INDEX idx_recally_article_summary_trgm ON recally_article USING GIN (summary gin_trgm_ops);
CREATE INDEX idx_recally_article_tags_trgm ON recally_article USING GIN (tags gin_trgm_ops);
CREATE INDEX idx_recally_article_author_trgm ON recally_article USING GIN (author gin_trgm_ops);
