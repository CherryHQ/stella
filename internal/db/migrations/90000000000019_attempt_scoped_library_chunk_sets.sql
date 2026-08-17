-- +goose Up

-- A derivation identifies the immutable input and output recipe, while a
-- ChunkSet identifies one concrete build attempt. VLM OCR makes byte-identical
-- output across retries impossible to assume, so attempts must not share rows.
ALTER TABLE "library_chunk_set"
  DROP CONSTRAINT "library_chunk_set_file_derivation_key";

CREATE INDEX "idx_library_chunk_set_file_derivation_status"
  ON "library_chunk_set" ("file_id", "derivation_key", "status", "completed_at" DESC, "id" DESC);

CREATE INDEX "idx_library_chunk_set_building_updated"
  ON "library_chunk_set" ("updated_at", "file_id", "id")
  WHERE "status" = 'building';

-- +goose Down

DROP INDEX "idx_library_chunk_set_building_updated";
DROP INDEX "idx_library_chunk_set_file_derivation_status";

-- Down can only restore the historical uniqueness after retaining one set per
-- derivation. Prefer the active set, then a ready set, then the newest attempt.
DELETE FROM "library_chunk_set" AS candidate
USING (
  SELECT id
  FROM (
    SELECT
      chunk_set.id,
      row_number() OVER (
        PARTITION BY chunk_set.file_id, chunk_set.derivation_key
        ORDER BY
          (file.active_chunk_set_id = chunk_set.id) DESC,
          (chunk_set.status = 'ready') DESC,
          chunk_set.completed_at DESC NULLS LAST,
          chunk_set.created_at DESC,
          chunk_set.id DESC
      ) AS position
    FROM "library_chunk_set" AS chunk_set
    JOIN "library_file" AS file ON file.id = chunk_set.file_id
  ) AS ranked
  WHERE ranked.position > 1
) AS duplicate
WHERE candidate.id = duplicate.id;

ALTER TABLE "library_chunk_set"
  ADD CONSTRAINT "library_chunk_set_file_derivation_key" UNIQUE ("file_id", "derivation_key");
