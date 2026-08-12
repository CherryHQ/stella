-- +goose Up
-- Keep a default while old stellad processes may still be running. They do not
-- publish with the locator-aware digest, and a new worker will reset and rebuild
-- any such unfinished generation after deployment converges.
ALTER TABLE library_chunk
ADD COLUMN locator_sha256 BYTEA DEFAULT decode(repeat('00', 32), 'hex');

UPDATE library_chunk
SET locator_sha256 = sha256(convert_to(locator::text, 'UTF8'));

ALTER TABLE library_chunk ALTER COLUMN locator_sha256 SET NOT NULL;

-- During a rolling deployment an old worker omits locator_sha256. PostgreSQL
-- applies the zero default before BEFORE INSERT triggers, so replace only that
-- sentinel; new workers retain the digest of their exact canonical JSON bytes.
-- +goose StatementBegin
CREATE FUNCTION library_chunk_fill_locator_digest() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.locator_sha256 = decode(repeat('00', 32), 'hex') THEN
        NEW.locator_sha256 = sha256(convert_to(NEW.locator::text, 'UTF8'));
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER library_chunk_fill_locator_digest_before_insert
BEFORE INSERT ON library_chunk
FOR EACH ROW EXECUTE FUNCTION library_chunk_fill_locator_digest();

-- Old workers calculate and submit the pre-locator aggregate when they mark a
-- generation ready. Replace it at the database transition boundary so such a
-- generation cannot become publishable without locator integrity.
-- +goose StatementBegin
CREATE FUNCTION library_chunk_set_digest_on_ready() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'building' AND NEW.status = 'ready' THEN
        SELECT sha256(decode(coalesce(string_agg(
            lpad(to_hex(ordinal), 16, '0') || encode(content_sha256, 'hex') || encode(locator_sha256, 'hex'),
            '' ORDER BY ordinal
        ), ''), 'hex'))
        INTO NEW.content_digest
        FROM library_chunk
        WHERE chunk_set_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER library_chunk_set_digest_before_ready
BEFORE UPDATE OF status ON library_chunk_set
FOR EACH ROW EXECUTE FUNCTION library_chunk_set_digest_on_ready();

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
DROP TRIGGER library_chunk_set_digest_before_ready ON library_chunk_set;
DROP FUNCTION library_chunk_set_digest_on_ready();
DROP TRIGGER library_chunk_fill_locator_digest_before_insert ON library_chunk;
DROP FUNCTION library_chunk_fill_locator_digest();

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
