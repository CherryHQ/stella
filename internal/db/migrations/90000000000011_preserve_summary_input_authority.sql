-- +goose Up
ALTER TABLE ctx_summary
ADD COLUMN contains_non_principal_input BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE ctx_summary
DROP COLUMN contains_non_principal_input;
