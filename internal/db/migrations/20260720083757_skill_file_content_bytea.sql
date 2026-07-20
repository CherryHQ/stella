-- +goose Up
-- Skill archives may contain binary files (images, fonts, data files); a text
-- column rejects NUL bytes and invalid UTF-8 at insert. Existing rows are valid
-- UTF-8 text, so the conversion is lossless.
ALTER TABLE skill_file ALTER COLUMN content TYPE bytea USING convert_to(content, 'UTF8');

-- +goose Down
-- Intentionally fails (loudly) if any row holds binary content: convert_from
-- rejects NUL bytes and invalid UTF-8, and silently mangling or dropping those
-- files would be worse. Delete binary skill files first if a downgrade is
-- genuinely required.
ALTER TABLE skill_file ALTER COLUMN content TYPE text USING convert_from(content, 'UTF8');
