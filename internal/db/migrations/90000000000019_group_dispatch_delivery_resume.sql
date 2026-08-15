-- +goose Up
-- A group reply that was already generated and persisted must never be
-- regenerated because a platform send failed halfway through it. These two
-- columns are the durable half of that contract: `delivery_cursor` counts the
-- leading response chunks a publisher has confirmed the platform accepted, and
-- `delivery_complete` records that the whole response reached the platform.
-- A retry that finds `result_message_id` set and `delivery_complete` false
-- re-delivers the persisted text from the cursor instead of re-running the
-- agent.
--
-- Only counters live here. The response text itself is already durable as the
-- canonical event-log message `result_message_id` points at, and image/file
-- payloads are deliberately not persisted: a base64 attachment belongs in a
-- media artifact, never inline in a work-queue row.
ALTER TABLE ctx_group_dispatch
  ADD COLUMN delivery_cursor BIGINT NOT NULL DEFAULT 0
    CONSTRAINT ctx_group_dispatch_delivery_cursor_check CHECK (delivery_cursor >= 0),
  ADD COLUMN delivery_complete BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE ctx_group_dispatch
  DROP COLUMN delivery_cursor,
  DROP COLUMN delivery_complete;
