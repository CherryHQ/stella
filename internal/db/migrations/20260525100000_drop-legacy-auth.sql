-- Breaking migration: move all remaining data from legacy auth tables into the
-- new OIDC tables, update FK references in surviving tables, then drop the
-- legacy tables entirely.
--
-- INSERT OR IGNORE is used throughout so the migration is safe to run even
-- when the Go startup backfill already copied some rows.

PRAGMA foreign_keys = OFF;

-- ── Step 1: auth_users → auth_user ──────────────────────────────────────────
INSERT OR IGNORE INTO auth_user
    (id, email, name, default_agent_id, notify_identity_id,
     age_public_key, age_private_key, created_at, updated_at)
SELECT id, username, username, default_agent_id, NULL,
       age_public_key, age_private_key, created_at, updated_at
FROM auth_users;

-- ── Step 2: auth_identities → channel_identity ──────────────────────────────
INSERT OR IGNORE INTO channel_identity
    (id, user_id, platform, external_id, name, created_at, updated_at)
SELECT id, user_id, platform, external_id, name, linked_at, linked_at
FROM auth_identities;

-- ── Step 3: restore notify_identity_id (auth_identities id = channel_identity id) ──
UPDATE auth_user
SET notify_identity_id = (
    SELECT notify_identity_id FROM auth_users WHERE auth_users.id = auth_user.id
)
WHERE notify_identity_id IS NULL
  AND EXISTS (
    SELECT 1 FROM auth_users WHERE auth_users.id = auth_user.id
      AND auth_users.notify_identity_id IS NOT NULL
  );

-- ── Step 4: one org + membership per user (idempotent) ──────────────────────
INSERT OR IGNORE INTO auth_organization
    (id, name, external_id, source, created_at, updated_at)
SELECT lower(hex(randomblob(16))),
       username || ' (default)',
       id,
       'backfill',
       created_at,
       updated_at
FROM auth_users
WHERE id NOT IN (SELECT external_id FROM auth_organization WHERE source = 'backfill');

INSERT OR IGNORE INTO auth_membership
    (id, user_id, organization_id, role, is_active, created_at, updated_at)
SELECT lower(hex(randomblob(16))),
       u.id,
       o.id,
       CASE WHEN u.role = 'admin' THEN 'admin' ELSE 'member' END,
       u.is_active,
       u.created_at,
       u.updated_at
FROM auth_users u
JOIN auth_organization o ON o.external_id = u.id AND o.source = 'backfill'
WHERE u.id NOT IN (SELECT user_id FROM auth_membership);

-- ── Step 5: password hashes → auth_credential ───────────────────────────────
INSERT OR IGNORE INTO auth_credential (id, user_id, password_hash, created_at, updated_at)
SELECT lower(hex(randomblob(16))), id, password_hash, created_at, updated_at
FROM auth_users
WHERE password_hash != ''
  AND id NOT IN (SELECT user_id FROM auth_credential);

-- ── Step 6: assign org_id to settings resources ─────────────────────────────
UPDATE settings_agents
SET org_id = (SELECT id FROM auth_organization WHERE source = 'backfill' ORDER BY created_at ASC LIMIT 1)
WHERE org_id IS NULL;

UPDATE settings_providers
SET org_id = (SELECT id FROM auth_organization WHERE source = 'backfill' ORDER BY created_at ASC LIMIT 1)
WHERE org_id IS NULL;

UPDATE settings_channels
SET org_id = (SELECT id FROM auth_organization WHERE source = 'backfill' ORDER BY created_at ASC LIMIT 1)
WHERE org_id IS NULL;

UPDATE auth_policies
SET org_id = (SELECT id FROM auth_organization WHERE source = 'backfill' ORDER BY created_at ASC LIMIT 1)
WHERE org_id IS NULL;

-- ── Step 7: update auth_user_tokens FK auth_users → auth_user ───────────────
DROP INDEX IF EXISTS idx_auth_user_tokens_auto_active;
ALTER TABLE auth_user_tokens RENAME TO _auth_user_tokens_old;
CREATE TABLE auth_user_tokens (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name           TEXT NOT NULL DEFAULT '',
    token_hash     TEXT NOT NULL UNIQUE,
    token_prefix   TEXT NOT NULL DEFAULT '',
    auto_generated INTEGER NOT NULL DEFAULT 0,
    last_used_at   TEXT,
    expires_at     TEXT,
    rotated_at     TEXT,
    revoked_at     TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_auth_user_tokens_auto_active
    ON auth_user_tokens(user_id)
    WHERE auto_generated = 1 AND revoked_at IS NULL;
INSERT INTO auth_user_tokens SELECT * FROM _auth_user_tokens_old;
DROP TABLE _auth_user_tokens_old;

-- ── Step 8: update auth_user_agents FK ──────────────────────────────────────
ALTER TABLE auth_user_agents RENAME TO _auth_user_agents_old;
CREATE TABLE auth_user_agents (
    user_id  TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES settings_agents(id) ON DELETE CASCADE,
    PRIMARY KEY(user_id, agent_id)
);
INSERT INTO auth_user_agents SELECT * FROM _auth_user_agents_old;
DROP TABLE _auth_user_agents_old;

-- ── Step 9: update vault_entries FK ─────────────────────────────────────────
ALTER TABLE vault_entries RENAME TO _vault_entries_old;
CREATE TABLE vault_entries (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, name)
);
INSERT INTO vault_entries SELECT * FROM _vault_entries_old;
DROP TABLE _vault_entries_old;

-- ── Step 10: update share FK ────────────────────────────────────────────────
DROP INDEX IF EXISTS idx_share_user;
ALTER TABLE share RENAME TO _share_old;
CREATE TABLE share (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    media_type TEXT NOT NULL,
    content BLOB NOT NULL,
    expires_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_share_user ON share(user_id, created_at DESC);
INSERT INTO share SELECT * FROM _share_old;
DROP TABLE _share_old;

-- ── Step 11: update articles FK ─────────────────────────────────────────────
DROP INDEX IF EXISTS idx_articles_user_canonical;
DROP INDEX IF EXISTS idx_articles_user_status;
DROP INDEX IF EXISTS idx_articles_user_source;
DROP INDEX IF EXISTS idx_articles_user_starred;
DROP INDEX IF EXISTS idx_articles_saved_at;
ALTER TABLE articles RENAME TO _articles_old;
CREATE TABLE articles (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id      TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    url           TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    source_type   TEXT NOT NULL DEFAULT 'web'
                  CHECK (source_type IN ('web','twitter','youtube','github','rss','pdf')),
    title         TEXT NOT NULL DEFAULT '',
    author        TEXT NOT NULL DEFAULT '',
    summary       TEXT NOT NULL DEFAULT '',
    tags          TEXT NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'unread'
                  CHECK (status IN ('unread','read','archived')),
    starred       INTEGER NOT NULL DEFAULT 0,
    file_path     TEXT NOT NULL DEFAULT '',
    metadata      TEXT NOT NULL DEFAULT '{}',
    published_at  TEXT,
    saved_at      TEXT NOT NULL DEFAULT (datetime('now')),
    read_at       TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_articles_user_canonical ON articles (user_id, canonical_url);
CREATE INDEX idx_articles_user_status ON articles (user_id, status);
CREATE INDEX idx_articles_user_source ON articles (user_id, source_type);
CREATE INDEX idx_articles_user_starred ON articles (user_id, starred) WHERE starred = 1;
CREATE INDEX idx_articles_saved_at ON articles (saved_at);
INSERT INTO articles SELECT * FROM _articles_old;
DROP TABLE _articles_old;

-- ── Step 12: update agent_task FK ───────────────────────────────────────────
DROP INDEX IF EXISTS idx_agent_task_user_id_status;
DROP INDEX IF EXISTS idx_agent_task_status;
DROP INDEX IF EXISTS idx_agent_task_session_id;
DROP INDEX IF EXISTS idx_agent_task_scheduler_job_id;
DROP INDEX IF EXISTS idx_agent_task_scheduler_run_id;
DROP INDEX IF EXISTS idx_agent_task_agent_id;
ALTER TABLE agent_task RENAME TO _agent_task_old;
CREATE TABLE agent_task (
    id TEXT NOT NULL PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
    priority TEXT NOT NULL DEFAULT 'routine' CHECK (priority IN ('routine','urgent')),
    session_id TEXT,
    context TEXT NOT NULL DEFAULT '{}',
    review_request TEXT NOT NULL DEFAULT '{}',
    deps TEXT NOT NULL DEFAULT '[]',
    notify_at TEXT,
    scheduler_job_id TEXT REFERENCES sched_jobs(id) ON DELETE SET NULL,
    scheduler_run_id TEXT REFERENCES sched_job_runs(id) ON DELETE SET NULL,
    agent_id TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_agent_task_user_id_status ON agent_task(user_id, status);
CREATE INDEX idx_agent_task_status ON agent_task(status);
CREATE INDEX idx_agent_task_session_id ON agent_task(session_id);
CREATE INDEX idx_agent_task_scheduler_job_id ON agent_task(scheduler_job_id);
CREATE INDEX idx_agent_task_scheduler_run_id ON agent_task(scheduler_run_id);
CREATE INDEX idx_agent_task_agent_id ON agent_task(agent_id);
INSERT INTO agent_task SELECT * FROM _agent_task_old;
DROP TABLE _agent_task_old;

-- ── Step 13: update skills FK ───────────────────────────────────────────────
DROP INDEX IF EXISTS idx_skills_owner_name;
DROP INDEX IF EXISTS idx_skills_visibility;
ALTER TABLE skills RENAME TO _skills_old;
CREATE TABLE skills (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL CHECK (scope IN ('system','agent','user')),
    user_id     TEXT    REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT    REFERENCES settings_agents(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('draft','active','deprecated')),
    disable_model_invocation INTEGER NOT NULL DEFAULT 0,
    metadata    TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL)
    )
);
CREATE UNIQUE INDEX idx_skills_owner_name
    ON skills (name, scope, ifnull(user_id, 0), ifnull(agent_id, ''));
CREATE INDEX idx_skills_visibility
    ON skills (scope, user_id, agent_id);
INSERT INTO skills SELECT * FROM _skills_old;
DROP TABLE _skills_old;

-- Recreate skill_files so its FK points to the new skills table.
ALTER TABLE skill_files RENAME TO _skill_files_old;
CREATE TABLE skill_files (
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    path     TEXT NOT NULL,
    content  TEXT NOT NULL,
    PRIMARY KEY (skill_id, path)
);
INSERT INTO skill_files SELECT * FROM _skill_files_old;
DROP TABLE _skill_files_old;

-- ── Step 14: update rss_feeds FK ────────────────────────────────────────────
DROP INDEX IF EXISTS idx_rss_feeds_user_url;
ALTER TABLE rss_feeds RENAME TO _rss_feeds_old;
CREATE TABLE rss_feeds (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id        TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    check_interval  TEXT NOT NULL DEFAULT '1h',
    last_checked_at TEXT,
    last_etag       TEXT NOT NULL DEFAULT '',
    last_modified   TEXT NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_rss_feeds_user_url ON rss_feeds (user_id, url);
INSERT INTO rss_feeds SELECT * FROM _rss_feeds_old;
DROP TABLE _rss_feeds_old;

-- ── Step 15: update recally_digests FK ──────────────────────────────────────
DROP INDEX IF EXISTS idx_recally_digests_user_date;
DROP INDEX IF EXISTS idx_recally_digests_user_id;
ALTER TABLE recally_digests RENAME TO _recally_digests_old;
CREATE TABLE recally_digests (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    date       TEXT NOT NULL,
    narrative  TEXT NOT NULL DEFAULT '',
    saved_yesterday_count  INTEGER NOT NULL DEFAULT 0,
    unread_count           INTEGER NOT NULL DEFAULT 0,
    read_count             INTEGER NOT NULL DEFAULT 0,
    archived_count         INTEGER NOT NULL DEFAULT 0,
    starred_count          INTEGER NOT NULL DEFAULT 0,
    worth_revisiting_count INTEGER NOT NULL DEFAULT 0,
    total_articles         INTEGER NOT NULL DEFAULT 0,
    top_tags   TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_recally_digests_user_date ON recally_digests (user_id, date);
CREATE INDEX idx_recally_digests_user_id ON recally_digests (user_id);
INSERT INTO recally_digests SELECT * FROM _recally_digests_old;
DROP TABLE _recally_digests_old;

-- ── Step 16: drop legacy auth tables ────────────────────────────────────────
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS auth_identities;
DROP TABLE IF EXISTS auth_users;

PRAGMA foreign_keys = ON;
