-- Runtime-managed FTS5 index over recally_article metadata
-- (title/summary/tags/author). Executed via go:embed at startup
-- (internal/db/fts.go), NOT through Atlas migrations: Atlas's embedded dev
-- SQLite lacks the fts5 module, so this file is intentionally not imported
-- into main.sql. sqlc still reads it (schema glob) so search queries can
-- reference the virtual table; see recally_article_fts_sqlc.sql for the
-- sqlc-only view of fts5's hidden table-name column.
CREATE VIRTUAL TABLE IF NOT EXISTS recally_article_fts USING fts5(
    title,
    summary,
    tags,
    author,
    content='recally_article',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 1'
);

CREATE TRIGGER IF NOT EXISTS recally_article_fts_ai AFTER INSERT ON recally_article BEGIN
    INSERT INTO recally_article_fts(rowid, title, summary, tags, author)
    VALUES (new.rowid, new.title, new.summary, new.tags, new.author);
END;

CREATE TRIGGER IF NOT EXISTS recally_article_fts_ad AFTER DELETE ON recally_article BEGIN
    INSERT INTO recally_article_fts(recally_article_fts, rowid, title, summary, tags, author)
    VALUES ('delete', old.rowid, old.title, old.summary, old.tags, old.author);
END;

CREATE TRIGGER IF NOT EXISTS recally_article_fts_au AFTER UPDATE OF title, summary, tags, author ON recally_article BEGIN
    INSERT INTO recally_article_fts(recally_article_fts, rowid, title, summary, tags, author)
    VALUES ('delete', old.rowid, old.title, old.summary, old.tags, old.author);
    INSERT INTO recally_article_fts(rowid, title, summary, tags, author)
    VALUES (new.rowid, new.title, new.summary, new.tags, new.author);
END;
