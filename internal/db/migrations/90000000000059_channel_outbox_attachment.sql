-- +goose Up
-- Reply file attachments live in a bytea child of the outbox op rather than in
-- the payload JSONB: ListPendingChannelOutbox batches SELECT * on the parent
-- table and must not haul megabytes of file data per row; the attachment is
-- read lazily by the op that owns it. The trade-off is database size — WAL and
-- backups carry the bytes — so an external blob store is the upgrade only when
-- a real budget or lease-duration measurement says so.
CREATE TABLE channel_outbox_attachment (
    -- One attachment body per send_attachment op.
    outbox_id  UUID PRIMARY KEY REFERENCES channel_outbox(id) ON DELETE CASCADE,
    data       BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Group replies are not produced by an agent_run, so run_id cannot anchor
-- their lifecycle. Pin them to the group itself: deleting the group drops its
-- pending sends, while deleting a channel or dispatch row must not erase
-- delivery history.
ALTER TABLE channel_outbox
    ADD COLUMN group_id UUID REFERENCES ctx_group_state(id) ON DELETE CASCADE;
CREATE INDEX idx_channel_outbox_group ON channel_outbox (group_id) WHERE group_id IS NOT NULL;

-- +goose Down
ALTER TABLE channel_outbox DROP COLUMN group_id;
DROP TABLE channel_outbox_attachment;
