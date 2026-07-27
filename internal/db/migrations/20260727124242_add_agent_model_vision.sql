-- +goose Up
-- Auxiliary multimodal model used to render images as text for agents whose
-- main model cannot accept image input. Empty means no vision model, which is
-- the correct default: image understanding then degrades to local extraction.
ALTER TABLE "agent" ADD COLUMN "model_vision" text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE "agent" DROP COLUMN "model_vision";
