-- +goose Up
CREATE TABLE agent_knowledge (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    kind        TEXT NOT NULL,
    scope       TEXT NOT NULL,
    user_id     UUID NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT NULL REFERENCES agent(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    content     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'draft',
    evidence    JSONB NOT NULL DEFAULT '[]',
    confidence  DOUBLE PRECISION NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    expires_at  TIMESTAMPTZ NULL,
    supersedes  UUID NULL REFERENCES agent_knowledge(id) ON DELETE SET NULL,
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (((scope = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
        OR ((scope = 'user_agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
        OR ((scope = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
        OR ((scope = 'system_agent') AND (user_id IS NULL) AND (agent_id IS NOT NULL))),
    CHECK (supersedes IS NULL OR supersedes <> id)
);

CREATE INDEX idx_agent_knowledge_visibility ON agent_knowledge (scope, user_id, agent_id);
CREATE INDEX idx_agent_knowledge_active ON agent_knowledge (kind, updated_at DESC)
    WHERE status = 'active';
CREATE INDEX idx_agent_knowledge_expires_at ON agent_knowledge (expires_at)
    WHERE expires_at IS NOT NULL;
CREATE INDEX idx_agent_knowledge_supersedes ON agent_knowledge (supersedes)
    WHERE supersedes IS NOT NULL;
CREATE UNIQUE INDEX uniq_agent_knowledge_owner_kind_name ON agent_knowledge (
    scope,
    COALESCE(user_id::TEXT, ''),
    COALESCE(agent_id, ''),
    kind,
    name
)
WHERE status <> 'deprecated';

-- +goose StatementBegin
DO $$
DECLARE
    offending_skill TEXT;
BEGIN
    SELECT s.id::TEXT
    INTO offending_skill
    FROM skill s
    WHERE s.disable_model_invocation = true
      AND s.metadata->>'knowledge_type' IN ('fact', 'context')
      AND NOT EXISTS (
          SELECT 1 FROM skill_file sf WHERE sf.skill_id = s.id AND sf.path = 'SKILL.md'
      )
    LIMIT 1;

    IF offending_skill IS NOT NULL THEN
        RAISE EXCEPTION
            'cannot auto-migrate legacy knowledge skill % because it has no SKILL.md file; repair or remove it before applying this migration',
            offending_skill;
    END IF;

    SELECT s.id::TEXT
    INTO offending_skill
    FROM skill s
    JOIN skill_file sf ON sf.skill_id = s.id
    WHERE s.disable_model_invocation = true
      AND s.metadata->>'knowledge_type' IN ('fact', 'context')
      AND sf.path <> 'SKILL.md'
    LIMIT 1;

    IF offending_skill IS NOT NULL THEN
        RAISE EXCEPTION
            'cannot auto-migrate legacy knowledge skill % because it has non-SKILL.md files; migrate those attachments manually before applying this migration',
            offending_skill;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    duplicate_key TEXT;
BEGIN
    SELECT concat_ws(
        '/',
        scope,
        COALESCE(user_id::TEXT, ''),
        COALESCE(agent_id, ''),
        metadata->>'knowledge_type',
        name
    )
    INTO duplicate_key
    FROM skill
    WHERE disable_model_invocation = true
      AND metadata->>'knowledge_type' IN ('fact', 'context')
      AND status <> 'deprecated'
    GROUP BY scope, COALESCE(user_id::TEXT, ''), COALESCE(agent_id, ''), metadata->>'knowledge_type', name
    HAVING count(*) > 1
    LIMIT 1;

    IF duplicate_key IS NOT NULL THEN
        RAISE EXCEPTION
            'cannot auto-migrate duplicate active/draft legacy knowledge key %; merge or deprecate duplicates before applying this migration',
            duplicate_key;
    END IF;
END $$;
-- +goose StatementEnd

WITH migrated AS (
    INSERT INTO agent_knowledge (
        kind,
        scope,
        user_id,
        agent_id,
        name,
        content,
        status,
        metadata,
        created_at,
        updated_at
    )
    SELECT
        s.metadata->>'knowledge_type',
        s.scope,
        s.user_id,
        s.agent_id,
        s.name,
        sf.content,
        s.status,
        s.metadata || jsonb_build_object('legacy_skill_id', s.id, 'legacy_description', s.description),
        s.created_at,
        s.updated_at
    FROM skill s
    JOIN skill_file sf ON sf.skill_id = s.id AND sf.path = 'SKILL.md'
    WHERE s.disable_model_invocation = true
      AND s.metadata->>'knowledge_type' IN ('fact', 'context')
    RETURNING metadata->>'legacy_skill_id' AS legacy_skill_id
)
DELETE FROM skill s
USING migrated
WHERE s.id = migrated.legacy_skill_id;

-- +goose Down
INSERT INTO skill (
    id,
    scope,
    user_id,
    agent_id,
    name,
    description,
    status,
    disable_model_invocation,
    metadata,
    created_at,
    updated_at
)
SELECT
    k.metadata->>'legacy_skill_id',
    k.scope,
    k.user_id,
    k.agent_id,
    k.name,
    COALESCE(k.metadata->>'legacy_description', ''),
    k.status,
    true,
    k.metadata - 'legacy_skill_id' - 'legacy_description',
    k.created_at,
    k.updated_at
FROM agent_knowledge k
WHERE k.metadata ? 'legacy_skill_id'
  AND NOT EXISTS (
      SELECT 1 FROM skill s WHERE s.id = k.metadata->>'legacy_skill_id'
  );

INSERT INTO skill_file (skill_id, path, content)
SELECT
    k.metadata->>'legacy_skill_id',
    'SKILL.md',
    k.content
FROM agent_knowledge k
WHERE k.metadata ? 'legacy_skill_id'
ON CONFLICT (skill_id, path) DO NOTHING;

DROP TABLE agent_knowledge;
