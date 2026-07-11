-- +goose Up
-- Make a group session's group identity durable on its LCM conversation row.
-- Until now group_id lived only on the in-flight runtime Info and was never
-- persisted, so a reloaded group conversation was indistinguishable from a
-- private one (reflect misclassified it; scope relied on the undeclared
-- user_id == group_id convention). Add an explicit, FK-backed column.
--
-- ON DELETE CASCADE matches every sibling group_id FK into ctx_group_state
-- (channel_group_member, ctx_group_dispatch, ctx_group_ingest_cursor/error,
-- ctx_group_memory, ctx_group_message, ctx_group_outbox): ctx_group_state is the
-- durable identity anchor for a group and its deletion already cascades all
-- group-derived state. The LCM conversation shell is likewise group-derived;
-- SET NULL would leave a corrupt "private" conversation still owned by a dead
-- group UUID (user_id), and RESTRICT would block group deletion on a subordinate
-- table. CASCADE also cleans the conversation's ctx_message/ctx_item rows, which
-- already cascade from ctx_conversation.
ALTER TABLE "ctx_conversation" ADD COLUMN "group_id" uuid NULL;
ALTER TABLE "ctx_conversation"
  ADD CONSTRAINT "ctx_conversation_group_id_fkey"
  FOREIGN KEY ("group_id") REFERENCES "ctx_group_state" ("id")
  ON UPDATE NO ACTION ON DELETE CASCADE;
-- Structural ownership invariant (a coupling rule between columns, not an enum):
-- a group conversation is owned by its group, so when group_id is set the row's
-- user_id must equal it. This is the DB-level twin of session.Info.Validate and
-- keeps a mismatched (user_id, group_id) pair unrepresentable at the boundary.
ALTER TABLE "ctx_conversation"
  ADD CONSTRAINT "ctx_conversation_group_owner_check"
  CHECK ("group_id" IS NULL OR ("user_id" IS NOT NULL AND "user_id" = "group_id"::text));
CREATE INDEX "idx_ctx_conversation_group_id" ON "ctx_conversation" ("group_id")
  WHERE ("group_id" IS NOT NULL);

-- Backfill only canonical group rows: user_id holds the group UUID (compared as
-- text to avoid an invalid-uuid cast on legacy non-uuid user_ids) and the
-- session_id is the derived group key agent_id || ':group:' || group UUID. Both
-- conditions must hold, so private conversations and any malformed row are left
-- untouched (group_id stays NULL).
UPDATE "ctx_conversation" AS c
SET "group_id" = gs."id",
    "updated_at" = now()
FROM "ctx_group_state" AS gs
WHERE c."group_id" IS NULL
  AND c."user_id" IS NOT NULL
  AND c."agent_id" IS NOT NULL
  AND gs."id"::text = c."user_id"
  AND c."session_id" = c."agent_id" || ':group:' || c."user_id";

-- +goose Down
ALTER TABLE "ctx_conversation" DROP CONSTRAINT IF EXISTS "ctx_conversation_group_owner_check";
ALTER TABLE "ctx_conversation" DROP CONSTRAINT IF EXISTS "ctx_conversation_group_id_fkey";
DROP INDEX IF EXISTS "idx_ctx_conversation_group_id";
ALTER TABLE "ctx_conversation" DROP COLUMN IF EXISTS "group_id";
