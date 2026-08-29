-- +goose Up
-- The baseline is a property of the media object, not of the message block that
-- happens to carry it. Storing it per block meant one image forwarded into two
-- messages was described twice by a VLM; keyed here on the ctx_media row, the
-- (owner, sha256) identity that already deduplicates the bytes deduplicates the
-- render too. NULL means "never rendered successfully"; the empty string is
-- forbidden so "rendered to nothing" can never masquerade as a result.
--
-- First write wins and the value is then immutable: readers race to describe the
-- same image, and a second description of unchanged bytes is noise, not news.
SET LOCAL lock_timeout = '5s';

ALTER TABLE ctx_media
    ADD COLUMN baseline TEXT;

ALTER TABLE ctx_media
    ADD CONSTRAINT ctx_media_baseline_nonempty CHECK (baseline IS NULL OR baseline <> '');

-- Backfill from the two places the baseline used to live. Direct sessions kept
-- it in the image part's text projection; the stable "unavailable" marker is a
-- rendering failure, not a baseline, so it is excluded. The earliest-written
-- part wins, which is the same first-write-wins rule the runtime now applies.
UPDATE ctx_media m
SET baseline = earliest.text_content, updated_at = now()
FROM (
    SELECT DISTINCT ON (p.media_id) p.media_id, p.text_content
    FROM ctx_message_part p
    WHERE p.part_type = 'image'
      AND p.media_id IS NOT NULL
      AND p.text_content IS NOT NULL
      AND p.text_content <> ''
      AND p.text_content <> '[Image baseline unavailable.]'
    ORDER BY p.media_id, p.created_at ASC, p.ordinal ASC
) AS earliest
WHERE m.id = earliest.media_id AND m.baseline IS NULL;

-- Group sessions kept it inside the content_blocks JSONB array, on each
-- image_ref element. media_id is matched against a UUID shape first: a legacy
-- row with a malformed id must be skipped, not abort the migration.
UPDATE ctx_media m
SET baseline = earliest.baseline, updated_at = now()
FROM (
    SELECT DISTINCT ON (block->>'media_id')
        (block->>'media_id')::uuid AS media_id,
        block->>'baseline' AS baseline
    FROM ctx_group_message g,
         LATERAL jsonb_array_elements(g.content_blocks) AS block
    WHERE jsonb_typeof(g.content_blocks) = 'array'
      AND block->>'kind' = 'image_ref'
      AND block->>'media_id' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
      AND COALESCE(block->>'baseline', '') <> ''
    ORDER BY block->>'media_id', g.created_at ASC, g.seq ASC
) AS earliest
WHERE m.id = earliest.media_id AND m.baseline IS NULL;

-- +goose Down
-- The backfilled values all came from ctx_message_part.text_content and
-- ctx_group_message.content_blocks, which this migration does not touch, so
-- dropping the column loses nothing that cannot be read back from there.
SET LOCAL lock_timeout = '5s';

ALTER TABLE ctx_media
    DROP CONSTRAINT ctx_media_baseline_nonempty;

ALTER TABLE ctx_media
    DROP COLUMN baseline;
