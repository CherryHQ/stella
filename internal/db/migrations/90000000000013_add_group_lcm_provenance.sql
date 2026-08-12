-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE "ctx_group_message"
  ADD COLUMN "actor_display_name" text NULL;

ALTER TABLE "ctx_message"
  ADD COLUMN "origin_group_message_id" uuid NULL,
  ADD CONSTRAINT "ctx_message_origin_group_message_id_fkey"
    FOREIGN KEY ("origin_group_message_id")
    REFERENCES "ctx_group_message" ("id")
    ON UPDATE NO ACTION
    ON DELETE SET NULL
    NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE "ctx_message"
  DROP CONSTRAINT "ctx_message_origin_group_message_id_fkey",
  DROP COLUMN "origin_group_message_id";

ALTER TABLE "ctx_group_message"
  DROP COLUMN "actor_display_name";
