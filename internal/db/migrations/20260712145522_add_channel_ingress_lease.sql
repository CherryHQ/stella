-- +goose Up
-- #712 Item 3: explicit multi-replica channel-ingress leadership.
--
-- Every replica used to start every managed channel bot poller unconditionally,
-- so two replicas polling the same platform produced Telegram 409 conflicts and
-- duplicate delivery on weixin/qq/feishu. This table is a single-leader lease:
-- exactly one replica (the lease holder) runs all channel pollers at a time.
--
-- Singleton shape (mirrors authz_policy_revision): one row pinned to id = 1 by
-- the PRIMARY KEY + CHECK, seeded here. owner_id is the holder's stable process
-- id (''=free). lease_expires_at is set from the DATABASE clock (now() + ttl) on
-- every acquire/renew, so failover timing never depends on a replica's local
-- clock; a peer may reacquire once lease_expires_at is in the past. The lease is
-- always-on: a single-replica deployment simply wins it on the first attempt.
CREATE TABLE "channel_ingress_lease" (
  "id"               integer PRIMARY KEY DEFAULT 1,
  "owner_id"         text NOT NULL DEFAULT '',
  "acquired_at"      timestamptz,
  "heartbeat_at"     timestamptz,
  "lease_expires_at" timestamptz,
  "updated_at"       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT "channel_ingress_lease_singleton" CHECK ("id" = 1)
);
INSERT INTO "channel_ingress_lease" ("id") VALUES (1)
  ON CONFLICT ("id") DO NOTHING;

-- +goose Down
-- Reversible and safe: the table holds only transient leadership bookkeeping, no
-- durable domain data, so dropping it fully reverses this migration.
DROP TABLE IF EXISTS "channel_ingress_lease";
