-- ctx_group_state is the registry of physical group conversations Stella has
-- observed, one row per (platform, group, thread). A Telegram forum topic counts
-- as its own group (distinct thread id). It also allocates each group's ordering
-- sequence, so the row doubles as the per-group write lock.
CREATE TABLE ctx_group_state (
    -- internal stable handle; every group-scoped table references this id.
    id                 TEXT PRIMARY KEY,
    -- messaging platform: 'telegram' | 'feishu' | 'qq' | ...
    platform           TEXT NOT NULL,
    -- native group/chat id as the platform reports it.
    platform_group_id  TEXT NOT NULL,
    -- platform sub-thread/topic id (e.g. Telegram forum topic). Empty string (not
    -- NULL) when the group has no thread, so the UNIQUE below actually holds —
    -- SQLite treats NULLs as distinct in a unique index.
    platform_thread_id TEXT NOT NULL DEFAULT '',
    -- per-group monotonic ordering allocator. Bumped under this row's write lock:
    -- UPDATE ... SET next_seq = next_seq + 1 RETURNING next_seq (first message = 1).
    next_seq           BIGINT NOT NULL DEFAULT 0,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- human-readable display name (web groups require it; platform groups may populate later).
    group_name         TEXT NOT NULL DEFAULT '',
    -- web groups only: the user who created this group. NULL for platform-created groups.
    created_by_user_id TEXT REFERENCES auth_user(id) ON DELETE CASCADE,
    -- one registry row per physical group/thread, stable across all observing bots.
    UNIQUE (platform, platform_group_id, platform_thread_id)
);

CREATE INDEX idx_ctx_group_state_web_owner
    ON ctx_group_state(created_by_user_id)
    WHERE platform = 'web';
