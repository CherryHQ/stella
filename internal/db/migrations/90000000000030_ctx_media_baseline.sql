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
-- it in the image part's text projection. Only text that satisfies the baseline
-- contract may be adopted: the reader rejects anything else and treats the row
-- as never described, but this column is write-once on NULL, so a value the
-- reader rejects could never be replaced by a real render. Leaving it NULL is
-- what lets the next reader describe the image. The predicate mirrors
-- ai.ValidateImageBaselineText: a "## Text" header, exactly one "## Scene"
-- separator, both sections non-empty, and no further section inside the scene.
-- The stable "unavailable" marker fails it by construction. The earliest-written
-- part wins, which is the same first-write-wins rule the runtime now applies.
UPDATE ctx_media m
SET baseline = earliest.text_content, updated_at = now()
FROM (
    SELECT DISTINCT ON (p.media_id) p.media_id, p.text_content
    FROM ctx_message_part p,
         LATERAL (SELECT btrim(p.text_content) AS text) AS candidate
    WHERE p.part_type = 'image'
      AND p.media_id IS NOT NULL
      AND candidate.text LIKE E'## Text\n%'
      AND array_length(string_to_array(candidate.text, E'\n\n## Scene\n'), 1) = 2
      AND btrim(substr(split_part(candidate.text, E'\n\n## Scene\n', 1), 9)) <> ''
      AND btrim(split_part(candidate.text, E'\n\n## Scene\n', 2)) <> ''
      AND position(E'\n\n## ' IN split_part(candidate.text, E'\n\n## Scene\n', 2)) = 0
    ORDER BY p.media_id, p.created_at ASC, p.ordinal ASC
) AS earliest
WHERE m.id = earliest.media_id AND m.baseline IS NULL;

-- Group sessions kept it inside the content_blocks JSONB array, on each
-- image_ref element. media_id is matched against a UUID shape first: a legacy
-- row with a malformed id must be skipped, not abort the migration. The
-- baseline predicate is the same contract as above, for the same reason.
UPDATE ctx_media m
SET baseline = earliest.baseline, updated_at = now()
FROM (
    SELECT DISTINCT ON (block->>'media_id')
        (block->>'media_id')::uuid AS media_id,
        block->>'baseline' AS baseline
    FROM ctx_group_message g,
         LATERAL jsonb_array_elements(g.content_blocks) AS block,
         LATERAL (SELECT btrim(block->>'baseline') AS text) AS candidate
    WHERE jsonb_typeof(g.content_blocks) = 'array'
      AND block->>'kind' = 'image_ref'
      AND block->>'media_id' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
      AND candidate.text LIKE E'## Text\n%'
      AND array_length(string_to_array(candidate.text, E'\n\n## Scene\n'), 1) = 2
      AND btrim(substr(split_part(candidate.text, E'\n\n## Scene\n', 1), 9)) <> ''
      AND btrim(split_part(candidate.text, E'\n\n## Scene\n', 2)) <> ''
      AND position(E'\n\n## ' IN split_part(candidate.text, E'\n\n## Scene\n', 2)) = 0
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
