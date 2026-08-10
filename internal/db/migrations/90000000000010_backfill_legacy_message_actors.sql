-- +goose Up
-- Previous-GA rows do not contain per-message evidence of who authored a
-- user-role message. Conversation kind is not author provenance: owners could
-- send human messages to delegate, scheduler, and task conversations through
-- the old Session API. Keep the actor_type default added by migration 9
-- (human) rather than risk demoting historical principal input.
SELECT 1;

-- +goose Down
SELECT 1;
