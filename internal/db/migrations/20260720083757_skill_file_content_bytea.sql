-- +goose Up
-- Skill archives may contain binary files (images, fonts, data files); a text
-- column rejects NUL bytes and invalid UTF-8 at insert. Existing rows are valid
-- UTF-8 text, so the conversion is lossless.
ALTER TABLE skill_file ALTER COLUMN content TYPE bytea USING convert_to(content, 'UTF8');

-- +goose Down
ALTER TABLE skill_file ALTER COLUMN content TYPE text USING convert_from(content, 'UTF8');
