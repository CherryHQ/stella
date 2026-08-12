-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '15min';

ALTER TABLE "ctx_message"
  VALIDATE CONSTRAINT "ctx_message_origin_group_message_id_fkey";

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE "ctx_message"
  DROP CONSTRAINT "ctx_message_origin_group_message_id_fkey",
  ADD CONSTRAINT "ctx_message_origin_group_message_id_fkey"
    FOREIGN KEY ("origin_group_message_id")
    REFERENCES "ctx_group_message" ("id")
    ON UPDATE NO ACTION
    ON DELETE SET NULL
    NOT VALID;
