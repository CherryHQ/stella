-- +goose Up

-- Migration 90000000000001 shipped in v0.61.0, so rename its schema in place
-- instead of rewriting applied history. PostgreSQL preserves rows and foreign
-- key targets while the explicit constraint/index renames remove the old
-- Knowledge Base terminology from the live schema.
ALTER TABLE "knowledge_file" RENAME TO "library_file";
ALTER TABLE "knowledge_chunk_set" RENAME TO "library_chunk_set";
ALTER TABLE "knowledge_chunk" RENAME TO "library_chunk";

ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_pkey" TO "library_file_pkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_owner_check" TO "library_file_owner_check";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_size_check" TO "library_file_size_check";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_user_id_fkey" TO "library_file_user_id_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_agent_id_fkey" TO "library_file_agent_id_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_active_chunk_set_fkey" TO "library_file_active_chunk_set_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_id_not_null" TO "library_file_id_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_scope_not_null" TO "library_file_scope_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_file_name_not_null" TO "library_file_file_name_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_media_type_not_null" TO "library_file_media_type_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_size_bytes_not_null" TO "library_file_size_bytes_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_raw_sha256_not_null" TO "library_file_raw_sha256_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_status_not_null" TO "library_file_status_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_created_at_not_null" TO "library_file_created_at_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "knowledge_file_updated_at_not_null" TO "library_file_updated_at_not_null";

ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_pkey" TO "library_chunk_set_pkey";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_file_id_fkey" TO "library_chunk_set_file_id_fkey";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_file_derivation_key" TO "library_chunk_set_file_derivation_key";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_file_id_id_key" TO "library_chunk_set_file_id_id_key";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_id_not_null" TO "library_chunk_set_id_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_file_id_not_null" TO "library_chunk_set_file_id_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_derivation_key_not_null" TO "library_chunk_set_derivation_key_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_processor_key_not_null" TO "library_chunk_set_processor_key_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_raw_sha256_not_null" TO "library_chunk_set_raw_sha256_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_status_not_null" TO "library_chunk_set_status_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_created_at_not_null" TO "library_chunk_set_created_at_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "knowledge_chunk_set_updated_at_not_null" TO "library_chunk_set_updated_at_not_null";

ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_pkey" TO "library_chunk_pkey";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_set_id_fkey" TO "library_chunk_set_id_fkey";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_set_ordinal_key" TO "library_chunk_set_ordinal_key";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_id_not_null" TO "library_chunk_id_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_chunk_set_id_not_null" TO "library_chunk_chunk_set_id_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_ordinal_not_null" TO "library_chunk_ordinal_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_content_not_null" TO "library_chunk_content_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_locator_not_null" TO "library_chunk_locator_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_content_sha256_not_null" TO "library_chunk_content_sha256_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_created_at_not_null" TO "library_chunk_created_at_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "knowledge_chunk_updated_at_not_null" TO "library_chunk_updated_at_not_null";

ALTER INDEX "idx_knowledge_file_user_id" RENAME TO "idx_library_file_user_id";
ALTER INDEX "idx_knowledge_file_agent_id" RENAME TO "idx_library_file_agent_id";
ALTER INDEX "idx_knowledge_file_user_owner" RENAME TO "idx_library_file_user_owner";
ALTER INDEX "idx_knowledge_file_agent_owner" RENAME TO "idx_library_file_agent_owner";
ALTER INDEX "idx_knowledge_file_system_created" RENAME TO "idx_library_file_system_created";
ALTER INDEX "idx_knowledge_file_processing" RENAME TO "idx_library_file_processing";
ALTER INDEX "idx_knowledge_file_tombstone" RENAME TO "idx_library_file_tombstone";
ALTER INDEX "idx_knowledge_chunk_bm25" RENAME TO "idx_library_chunk_bm25";

-- +goose Down

ALTER INDEX "idx_library_file_user_id" RENAME TO "idx_knowledge_file_user_id";
ALTER INDEX "idx_library_file_agent_id" RENAME TO "idx_knowledge_file_agent_id";
ALTER INDEX "idx_library_file_user_owner" RENAME TO "idx_knowledge_file_user_owner";
ALTER INDEX "idx_library_file_agent_owner" RENAME TO "idx_knowledge_file_agent_owner";
ALTER INDEX "idx_library_file_system_created" RENAME TO "idx_knowledge_file_system_created";
ALTER INDEX "idx_library_file_processing" RENAME TO "idx_knowledge_file_processing";
ALTER INDEX "idx_library_file_tombstone" RENAME TO "idx_knowledge_file_tombstone";
ALTER INDEX "idx_library_chunk_bm25" RENAME TO "idx_knowledge_chunk_bm25";

ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_pkey" TO "knowledge_chunk_pkey";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_set_id_fkey" TO "knowledge_chunk_set_id_fkey";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_set_ordinal_key" TO "knowledge_chunk_set_ordinal_key";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_id_not_null" TO "knowledge_chunk_id_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_chunk_set_id_not_null" TO "knowledge_chunk_chunk_set_id_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_ordinal_not_null" TO "knowledge_chunk_ordinal_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_content_not_null" TO "knowledge_chunk_content_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_locator_not_null" TO "knowledge_chunk_locator_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_content_sha256_not_null" TO "knowledge_chunk_content_sha256_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_created_at_not_null" TO "knowledge_chunk_created_at_not_null";
ALTER TABLE "library_chunk" RENAME CONSTRAINT "library_chunk_updated_at_not_null" TO "knowledge_chunk_updated_at_not_null";

ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_pkey" TO "knowledge_chunk_set_pkey";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_file_id_fkey" TO "knowledge_chunk_set_file_id_fkey";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_file_derivation_key" TO "knowledge_chunk_set_file_derivation_key";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_file_id_id_key" TO "knowledge_chunk_set_file_id_id_key";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_id_not_null" TO "knowledge_chunk_set_id_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_file_id_not_null" TO "knowledge_chunk_set_file_id_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_derivation_key_not_null" TO "knowledge_chunk_set_derivation_key_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_processor_key_not_null" TO "knowledge_chunk_set_processor_key_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_raw_sha256_not_null" TO "knowledge_chunk_set_raw_sha256_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_status_not_null" TO "knowledge_chunk_set_status_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_created_at_not_null" TO "knowledge_chunk_set_created_at_not_null";
ALTER TABLE "library_chunk_set" RENAME CONSTRAINT "library_chunk_set_updated_at_not_null" TO "knowledge_chunk_set_updated_at_not_null";

ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_pkey" TO "knowledge_file_pkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_owner_check" TO "knowledge_file_owner_check";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_size_check" TO "knowledge_file_size_check";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_user_id_fkey" TO "knowledge_file_user_id_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_agent_id_fkey" TO "knowledge_file_agent_id_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_active_chunk_set_fkey" TO "knowledge_file_active_chunk_set_fkey";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_id_not_null" TO "knowledge_file_id_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_scope_not_null" TO "knowledge_file_scope_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_file_name_not_null" TO "knowledge_file_file_name_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_media_type_not_null" TO "knowledge_file_media_type_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_size_bytes_not_null" TO "knowledge_file_size_bytes_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_raw_sha256_not_null" TO "knowledge_file_raw_sha256_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_status_not_null" TO "knowledge_file_status_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_created_at_not_null" TO "knowledge_file_created_at_not_null";
ALTER TABLE "library_file" RENAME CONSTRAINT "library_file_updated_at_not_null" TO "knowledge_file_updated_at_not_null";

ALTER TABLE "library_chunk" RENAME TO "knowledge_chunk";
ALTER TABLE "library_chunk_set" RENAME TO "knowledge_chunk_set";
ALTER TABLE "library_file" RENAME TO "knowledge_file";
