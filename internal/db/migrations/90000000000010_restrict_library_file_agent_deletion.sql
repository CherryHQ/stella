-- +goose Up
ALTER TABLE "library_file"
  DROP CONSTRAINT "library_file_agent_id_fkey",
  ADD CONSTRAINT "library_file_agent_id_fkey"
  FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") NOT VALID;

ALTER TABLE "library_file"
  VALIDATE CONSTRAINT "library_file_agent_id_fkey";

-- +goose Down
ALTER TABLE "library_file"
  DROP CONSTRAINT "library_file_agent_id_fkey",
  ADD CONSTRAINT "library_file_agent_id_fkey"
  FOREIGN KEY ("agent_id") REFERENCES "agent" ("id") ON DELETE CASCADE;
