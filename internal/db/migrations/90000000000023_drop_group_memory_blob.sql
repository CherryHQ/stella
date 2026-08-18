-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The canonical public event log plus model-initiated recall replaces this
-- unstructured derived drawer. Its data is intentionally not migrated.
DROP TABLE "ctx_group_memory";

-- +goose Down
SET LOCAL lock_timeout = '5s';

CREATE TABLE "ctx_group_memory" (
  "group_id" uuid NOT NULL,
  "content" text NOT NULL DEFAULT '',
  "version" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("group_id"),
  CONSTRAINT "ctx_group_memory_group_id_fkey"
    FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id")
    ON UPDATE NO ACTION ON DELETE CASCADE
);
