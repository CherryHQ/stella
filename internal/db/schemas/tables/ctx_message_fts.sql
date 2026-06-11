-- Runtime-managed FTS5 index over ctx_message.content.
-- Executed via go:embed at startup (internal/db/fts.go), NOT through Atlas
-- migrations: Atlas's embedded dev SQLite lacks the fts5 module, so this file
-- is intentionally not imported into main.sql. sqlc still reads it (schema
-- glob) so search queries can reference the virtual table.
CREATE VIRTUAL TABLE IF NOT EXISTS ctx_message_fts USING fts5(
    content,
    content='ctx_message',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 1'
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
