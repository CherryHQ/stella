-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE ctx_media
    DROP COLUMN width_px,
    DROP COLUMN height_px;
UPDATE ctx_message_part
SET text_content = '[Image baseline unavailable.]'
WHERE part_type = 'image'
  AND baseline_status = 'unavailable';
ALTER TABLE ctx_message_part
    DROP COLUMN baseline_status,
    DROP COLUMN baseline_renderer,
    DROP COLUMN baseline_contract;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE ctx_media
    ADD COLUMN width_px INTEGER NOT NULL DEFAULT 1 CHECK (width_px > 0),
    ADD COLUMN height_px INTEGER NOT NULL DEFAULT 1 CHECK (height_px > 0);
ALTER TABLE ctx_message_part
    ADD COLUMN baseline_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN baseline_renderer TEXT NOT NULL DEFAULT '',
    ADD COLUMN baseline_contract INTEGER NOT NULL DEFAULT 0 CHECK (baseline_contract >= 0);
UPDATE ctx_message_part
SET baseline_status = CASE
        WHEN text_content = '[Image baseline unavailable.]' THEN 'unavailable'
        ELSE 'ready'
    END,
    baseline_renderer = CASE
        WHEN text_content = '[Image baseline unavailable.]' THEN ''
        ELSE 'migration/unknown'
    END,
    baseline_contract = CASE
        WHEN text_content = '[Image baseline unavailable.]' THEN 0
        ELSE 1
    END
WHERE part_type = 'image';
