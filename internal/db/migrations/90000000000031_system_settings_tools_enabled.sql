-- +goose Up
-- Settings-tool discovery is an Agent capability. Existing rows start disabled
-- so deployment upgrades never grant a conversational management surface.
ALTER TABLE agent
    ADD COLUMN system_settings_tools_enabled BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent
    DROP COLUMN system_settings_tools_enabled;
