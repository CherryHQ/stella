-- Runtime-managed FTS5 index over ctx_message.content.
-- Executed via go:embed at startup (internal/db/fts.go), NOT through Atlas
-- migrations: Atlas's embedded dev SQLite lacks the fts5 module, so this file
-- is intentionally not imported into main.sql. sqlc still reads it (schema
-- glob) so search queries can reference the virtual table.
-- trigram tokenizer: unicode61 indexed contiguous CJK runs as single tokens,
-- so 部署 never matched 今天讨论了部署方案. Trigram gives CJK (and English)
-- substring recall with BM25 + snippets; queries under 3 runes silently match
-- nothing, so callers fall back to LIKE on the content table.
-- internal/db/fts.go drops and rebuilds the index when an existing table
-- still carries a different tokenizer.
CREATE VIRTUAL TABLE IF NOT EXISTS ctx_message_fts USING fts5(
    content,
    content='ctx_message',
    content_rowid='rowid',
    tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS ctx_message_fts_ai AFTER INSERT ON ctx_message BEGIN
    INSERT INTO ctx_message_fts(rowid, content) VALUES (new.rowid, new.content);
END;

CREATE TRIGGER IF NOT EXISTS ctx_message_fts_ad AFTER DELETE ON ctx_message BEGIN
    INSERT INTO ctx_message_fts(ctx_message_fts, rowid, content)
    VALUES ('delete', old.rowid, old.content);
END;

CREATE TRIGGER IF NOT EXISTS ctx_message_fts_au AFTER UPDATE OF content ON ctx_message BEGIN
    INSERT INTO ctx_message_fts(ctx_message_fts, rowid, content)
    VALUES ('delete', old.rowid, old.content);
    INSERT INTO ctx_message_fts(rowid, content) VALUES (new.rowid, new.content);
END;
