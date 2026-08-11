-- +goose Up
-- Keep a default while old stellad processes may still be running. They do not
-- publish with the locator-aware digest, and a new worker will reset and rebuild
-- any such unfinished generation after deployment converges.
ALTER TABLE library_chunk
ADD COLUMN locator_sha256 BYTEA DEFAULT decode(repeat('00', 32), 'hex');

UPDATE library_chunk
SET locator_sha256 = sha256(convert_to(locator::text, 'UTF8'));

ALTER TABLE library_chunk ALTER COLUMN locator_sha256 SET NOT NULL;

UPDATE library_chunk_set AS chunk_set
SET content_digest = digest.content_digest
FROM (
    SELECT chunk_set_id,
        sha256(decode(coalesce(string_agg(
            lpad(to_hex(ordinal), 16, '0') || encode(content_sha256, 'hex') || encode(locator_sha256, 'hex'),
            '' ORDER BY ordinal
        ), ''), 'hex')) AS content_digest
    FROM library_chunk
    GROUP BY chunk_set_id
) AS digest
WHERE chunk_set.id = digest.chunk_set_id;

-- +goose Down
UPDATE library_chunk_set AS chunk_set
SET content_digest = digest.content_digest
FROM (
    SELECT chunk_set_id,
        sha256(decode(coalesce(string_agg(
            lpad(to_hex(ordinal), 16, '0') || encode(content_sha256, 'hex'),
            '' ORDER BY ordinal
        ), ''), 'hex')) AS content_digest
    FROM library_chunk
    GROUP BY chunk_set_id
) AS digest
WHERE chunk_set.id = digest.chunk_set_id;

ALTER TABLE library_chunk DROP COLUMN locator_sha256;
