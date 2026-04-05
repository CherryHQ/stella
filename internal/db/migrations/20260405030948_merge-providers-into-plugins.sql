-- Migrate provider credentials into settings_plugins before dropping the old table.
-- First, update existing plugin rows (from seeding) with credentials from settings_providers.
UPDATE settings_plugins SET
    config = (
        SELECT json_object('api_key', p.api_key, 'base_url', p.base_url, 'display_name', p.name)
        FROM settings_providers p
        WHERE settings_plugins.id = 'provider/' || p.id
    ),
    updated_at = (
        SELECT p.updated_at
        FROM settings_providers p
        WHERE settings_plugins.id = 'provider/' || p.id
    )
WHERE id IN (SELECT 'provider/' || p.id FROM settings_providers p)
  AND kind = 'provider';

-- Then insert any providers that don't yet have a plugin row.
INSERT OR IGNORE INTO settings_plugins (id, kind, name, enabled, config, created_at, updated_at)
SELECT
    'provider/' || p.id,
    'provider',
    p.id,
    1,
    json_object('api_key', p.api_key, 'base_url', p.base_url, 'display_name', p.name),
    p.created_at,
    p.updated_at
FROM settings_providers p;

-- Drop "settings_providers" table
DROP TABLE `settings_providers`;
