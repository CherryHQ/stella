package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// Keep these boundaries explicit. Advancing either one without adding the
	// representative fixture/assertions for the newly crossed migrations turns
	// this test into a green lie.
	previousGAVersion = int64(20260725161331)
	// Knowledge V1 and channel guest sessions are the post-anchor migrations
	// exercised by the assertions below.
	currentMigrationVersion = sequentialAnchor + 4

	previousGAUserID         = "00000000-0000-0000-0000-000000000001"
	previousGAGroupID        = "00000000-0000-0000-0000-000000000002"
	previousGAOlderChatID    = "00000000-0000-0000-0000-000000000009"
	previousGAOldChatID      = "00000000-0000-0000-0000-000000000003"
	previousGANewChatID      = "00000000-0000-0000-0000-000000000004"
	previousGAMessageID      = "00000000-0000-0000-0000-000000000005"
	previousGAPartID         = "00000000-0000-0000-0000-000000000006"
	previousGAMediaID        = "00000000-0000-0000-0000-000000000007"
	previousGAWebhookID      = "00000000-0000-0000-0000-000000000008"
	previousGAKnowledgeFile  = "00000000-0000-0000-0000-000000000041"
	previousGAChunkSet       = "00000000-0000-0000-0000-000000000042"
	previousGAChunk          = "00000000-0000-0000-0000-000000000043"
	previousGAGuestID        = "00000000-0000-0000-0000-000000000044"
	previousGAGuestChatID    = "00000000-0000-0000-0000-000000000045"
	previousGAAgentID        = "previous-ga-agent"
	previousGACascadeAgentID = "previous-ga-cascade-agent"
	previousGAProviderID     = "previous-ga-provider"
	previousGAOlderSession   = "previous-ga-agent:group:00000000-0000-0000-0000-000000000002:zz"
	previousGAOldSession     = "previous-ga-agent:group:00000000-0000-0000-0000-000000000002:a"
	previousGANewSession     = "previous-ga-agent:group:00000000-0000-0000-0000-000000000002:z"
)

var previousGATime = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// TestPreviousGAPostgresForwardMigration builds the exact v0.60.4 goose
// boundary from immutable migration history, then uses the production OpenDB
// path to upgrade persisted rows through every candidate migration.
func TestPreviousGAPostgresForwardMigration(t *testing.T) {
	ctx := context.Background()
	dsn, legacy := newPreviousGADB(t, ctx)
	seedPreviousGAData(t, ctx, legacy)
	legacy.Close()

	candidate, err := OpenDB(dsn, WithMaxConns(4))
	if err != nil {
		t.Fatalf("OpenDB upgrades v0.60.4 database: %v", err)
	}
	t.Cleanup(candidate.Close)

	assertPreviousGAUpgrade(t, ctx, candidate)
}

// newPreviousGADB intentionally starts with an empty database instead of
// rolling a current template back: Down migrations are not a historical schema
// reconstruction contract, while UpTo is the immutable v0.60.4 boundary.
func newPreviousGADB(t *testing.T, ctx context.Context) (string, *pgxpool.Pool) {
	t.Helper()
	pkgTestEnsure()
	if pkgTestErr != nil {
		t.Fatalf("start embedded PostgreSQL: %v", pkgTestErr)
	}

	name := fmt.Sprintf("previous_ga_%d", pkgTestSeq.Add(1))
	if _, err := pkgTestAdmin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create previous-GA database: %v", err)
	}
	dsn := pkgTestServer.DSNFor(name)
	legacy, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open previous-GA database: %v", err)
	}
	t.Cleanup(func() {
		legacy.Close()
		_, _ = pkgTestAdmin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		_, _ = pkgTestAdmin.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	})

	conn, err := legacy.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire previous-GA migration connection: %v", err)
	}
	err = ensureExtensions(ctx, conn)
	conn.Release()
	if err != nil {
		t.Fatalf("install previous-GA extensions: %v", err)
	}

	migrations, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(legacy)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, previousGAVersion); err != nil {
		t.Fatalf("migrate to v0.60.4 boundary: %v", err)
	}
	var appliedVersion int64
	if err := legacy.QueryRow(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&appliedVersion); err != nil {
		t.Fatalf("read previous-GA migration ledger: %v", err)
	}
	if appliedVersion != previousGAVersion {
		t.Fatalf("previous-GA migration boundary = %d, want %d", appliedVersion, previousGAVersion)
	}
	return dsn, legacy
}

func seedPreviousGAData(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	exec := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	exec("user", `INSERT INTO auth_user (id, email, created_at, updated_at) VALUES ($1, 'previous-ga@test.invalid', $2, $2)`, previousGAUserID, previousGATime)
	exec("personal access token", `INSERT INTO personal_access_token (id, public_id, user_id, name, token_hash, last4, scopes, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-000000000014', 'previous-ga-pat', $1, 'previous GA PAT', 'previous-ga-hash', '1234', ARRAY['goals:read'], $2, $2)`, previousGAUserID, previousGATime)
	exec("agents", `INSERT INTO agent (id, name, workspace, created_at, updated_at) VALUES
		($1, 'Previous GA Agent', '/tmp', $3, $3),
		($2, 'Previous GA Cascade Agent', '/tmp', $3, $3)`, previousGAAgentID, previousGACascadeAgentID, previousGATime)
	exec("canonical provider", `INSERT INTO provider (id, type, name, created_at, updated_at) VALUES ($1, 'anthropic', 'Previous GA Provider', $2, $2)`, previousGAProviderID, previousGATime)
	exec("sandbox plugin rows", `INSERT INTO plugin (id, kind, name, created_at, updated_at) VALUES
		('sandbox/local', 'sandbox', 'Local sandbox', $1, $1),
		('sandbox', 'sandbox', 'Sandbox near miss', $1, $1),
		('tool/unrelated', 'tool', 'Unrelated tool', $1, $1)`, previousGATime)
	exec("sandbox plugin state rows", `INSERT INTO plugin_state (plugin_id, scope_kind, state_key, value, created_at, updated_at) VALUES
		('sandbox/local', 'system', 'sandbox-state', '{"keep":"no"}', $1, $1),
		('sandbox', 'system', 'near-miss-state', '{"keep":"yes"}', $1, $1),
		('tool/unrelated', 'system', 'unrelated-state', '{"keep":"yes"}', $1, $1)`, previousGATime)
	exec("group", `INSERT INTO ctx_group_state (id, platform, platform_group_id, created_at, updated_at) VALUES ($1, 'test', 'previous-ga-group', $2, $2)`, previousGAGroupID, previousGATime)
	exec("duplicate group chats", `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id, group_id, created_at, updated_at)
		VALUES
			($1, $2, 'web', 'chat', false, $3, $4, $5::text, $6, $7, $7),
			($8, $9, 'web', 'chat', false, $10, $4, $5::text, $6, $7, $7),
			($11, $12, 'web', 'chat', false, $10, $4, $5::text, $6, $7, $7)`,
		previousGAOlderChatID, previousGAOlderSession, previousGATime.Add(-time.Hour), previousGAAgentID, previousGAGroupID, previousGAGroupID, previousGATime,
		previousGAOldChatID, previousGAOldSession, previousGATime,
		previousGANewChatID, previousGANewSession)
	exec("legacy message", `INSERT INTO ctx_message (id, conversation_id, seq, role, content, token_count, created_at) VALUES ($1, $2, 1, 'user', 'legacy media parent', 1, $3)`, previousGAMessageID, previousGANewChatID, previousGATime)
	exec("legacy message part", `INSERT INTO ctx_message_part (id, message_id, part_type, ordinal, text_content) VALUES ($1, $2, 'text', 0, 'legacy media child')`, previousGAPartID, previousGAMessageID)

	exec("vault entries", `
		INSERT INTO vault_entry (id, scope, name, ciphertext, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000011', 'system', 'LARK_CLI_OAUTH', 'lark-cipher', $1, $1),
			('00000000-0000-0000-0000-000000000012', 'system', 'FEISHU_CLI_OAUTH', 'feishu-cipher', $1, $1),
			('00000000-0000-0000-0000-000000000013', 'system', 'CUSTOM_SECRET', 'custom-cipher', $1, $1)`, previousGATime)
	exec("OAuth providers", `
		INSERT INTO plugin_oauth_provider (id, provider_id, client_id, scopes, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000021', 'lark', 'lark-client', ARRAY['calendar'], $1, $1),
			('00000000-0000-0000-0000-000000000022', 'feishu', 'feishu-client', ARRAY['drive'], $1, $1),
			('00000000-0000-0000-0000-000000000023', 'custom', 'custom-client', ARRAY['custom'], $1, $1)`, previousGATime)
	exec("Lark override", `
		INSERT INTO plugin_override (plugin_id, enabled, session_env_vault_key, config, created_at, updated_at)
		VALUES ('tool/lark-cli', true, 'custom-vault-key',
			'{"prompt":"custom prompt", "oauth_provider":"feishu", "session_env":{"TOKEN":"secret"}, "binary":"lark-cli", "custom":"keep"}', $1, $1)`, previousGATime)
	exec("unrelated override", `
		INSERT INTO plugin_override (plugin_id, enabled, config, created_at, updated_at)
		VALUES ('tool/custom', false, '{"custom":"untouched"}', $1, $1)`, previousGATime)
}

func assertPreviousGAUpgrade(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	count := func(name, query string, args ...any) int {
		t.Helper()
		var got int
		if err := db.QueryRow(ctx, query, args...).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		return got
	}
	var tokenUse string
	var issuedByProvisioning bool
	if err := db.QueryRow(ctx, `SELECT token_use, issued_by_provisioning FROM personal_access_token WHERE public_id = 'previous-ga-pat'`).Scan(&tokenUse, &issuedByProvisioning); err != nil {
		t.Fatalf("read migrated personal access token use: %v", err)
	}
	if tokenUse != "personal" || issuedByProvisioning {
		t.Fatalf("migrated personal access token use=%q issued_by_provisioning=%v, want personal/false", tokenUse, issuedByProvisioning)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO auth_provisioned_user (id, external_id, user_id, created_by_user_id, created_by_token_id)
		VALUES ('00000000-0000-0000-0000-000000000015', 'previous-ga-external', $1, $1, '00000000-0000-0000-0000-000000000014')`, previousGAUserID); err != nil {
		t.Fatalf("insert provisioned-user migration fixture: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE personal_access_token SET issued_by_token_id = id, issued_by_provisioning = true WHERE public_id = 'previous-ga-pat'`); err != nil {
		t.Fatalf("set provisioned PAT issuer migration fixture: %v", err)
	}
	if got := count("provisioned-user migration fixture", `SELECT count(*) FROM auth_provisioned_user WHERE external_id = 'previous-ga-external'`); got != 1 {
		t.Fatalf("provisioned-user rows = %d, want 1", got)
	}
	if got := count("retired vault entries", `SELECT count(*) FROM vault_entry WHERE name IN ('LARK_CLI_OAUTH', 'FEISHU_CLI_OAUTH')`); got != 0 {
		t.Fatalf("retired vault entries = %d, want 0", got)
	}
	if got := count("custom vault entry", `SELECT count(*) FROM vault_entry WHERE name = 'CUSTOM_SECRET'`); got != 1 {
		t.Fatalf("custom vault entries = %d, want 1", got)
	}
	if got := count("retired OAuth providers", `SELECT count(*) FROM plugin_oauth_provider WHERE provider_id IN ('lark', 'feishu')`); got != 0 {
		t.Fatalf("retired OAuth providers = %d, want 0", got)
	}
	if got := count("custom OAuth provider", `SELECT count(*) FROM plugin_oauth_provider WHERE provider_id = 'custom'`); got != 1 {
		t.Fatalf("custom OAuth providers = %d, want 1", got)
	}
	var larkClean, larkCustom, unrelatedPreserved bool
	if err := db.QueryRow(ctx, `
		SELECT NOT (config::jsonb ? 'oauth_provider') AND NOT (config::jsonb ? 'session_env'),
		       config::jsonb->>'prompt' = 'custom prompt' AND config::jsonb->>'custom' = 'keep' AND session_env_vault_key = 'custom-vault-key',
		       EXISTS (SELECT 1 FROM plugin_override WHERE plugin_id = 'tool/custom' AND enabled = false AND config::jsonb->>'custom' = 'untouched')
		FROM plugin_override WHERE plugin_id = 'tool/lark-cli'`).Scan(&larkClean, &larkCustom, &unrelatedPreserved); err != nil {
		t.Fatalf("read cleaned Lark override: %v", err)
	}
	if !larkClean || !larkCustom || !unrelatedPreserved {
		t.Fatalf("Lark cleanup = fields removed %v, custom preserved %v, unrelated preserved %v; want true, true, true", larkClean, larkCustom, unrelatedPreserved)
	}
	if got := count("deleted sandbox plugins", `SELECT count(*) FROM plugin WHERE id LIKE 'sandbox/%'`); got != 0 {
		t.Fatalf("sandbox plugin rows = %d, want 0", got)
	}
	if got := count("deleted sandbox plugin state", `SELECT count(*) FROM plugin_state WHERE plugin_id LIKE 'sandbox/%'`); got != 0 {
		t.Fatalf("sandbox plugin state rows = %d, want 0", got)
	}
	if got := count("sandbox plugin near miss", `SELECT count(*) FROM plugin WHERE id = 'sandbox'`); got != 1 {
		t.Fatalf("sandbox plugin near-miss rows = %d, want 1", got)
	}
	if got := count("sandbox plugin state near miss", `SELECT count(*) FROM plugin_state WHERE plugin_id = 'sandbox' AND state_key = 'near-miss-state'`); got != 1 {
		t.Fatalf("sandbox plugin state near-miss rows = %d, want 1", got)
	}
	if got := count("unrelated plugin rows", `SELECT count(*) FROM plugin WHERE id = 'tool/unrelated'`); got != 1 {
		t.Fatalf("unrelated plugin rows = %d, want 1", got)
	}
	if got := count("unrelated plugin state", `SELECT count(*) FROM plugin_state WHERE plugin_id = 'tool/unrelated' AND state_key = 'unrelated-state'`); got != 1 {
		t.Fatalf("unrelated plugin state rows = %d, want 1", got)
	}

	var olderArchived, oldArchived, newArchived bool
	if err := db.QueryRow(ctx, `SELECT archived FROM ctx_conversation WHERE session_id = $1`, previousGAOlderSession).Scan(&olderArchived); err != nil {
		t.Fatalf("read older duplicate: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT archived FROM ctx_conversation WHERE session_id = $1`, previousGAOldSession).Scan(&oldArchived); err != nil {
		t.Fatalf("read archived duplicate: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT archived FROM ctx_conversation WHERE session_id = $1`, previousGANewSession).Scan(&newArchived); err != nil {
		t.Fatalf("read retained duplicate: %v", err)
	}
	if !olderArchived || !oldArchived || newArchived {
		t.Fatalf("duplicate archival = older %v, tie loser %v, winner %v; want true, true, false (last_active DESC then session_id DESC)", olderArchived, oldArchived, newArchived)
	}
	_, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, group_id, last_active, created_at, updated_at) VALUES ('00000000-0000-0000-0000-000000000099', 'previous-ga-agent:group:duplicate', 'web', 'chat', $1, $2::text, $2::uuid, $3, $3, $3)`, previousGAAgentID, previousGAGroupID, previousGATime)
	assertConstraintViolation(t, err, "idx_one_agent_group_chat")
	if _, err := db.Exec(ctx, `INSERT INTO channel_chat_command_receipt (id, channel_id, chat_key, message_id, command, binding, created_at, updated_at) VALUES ('00000000-0000-0000-0000-000000000030', 'test-channel', 'test-chat', 'platform-message-1', '/new', $1, $2, $2)`, previousGAGroupID, previousGATime); err != nil {
		t.Fatalf("insert valid chat command receipt: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO channel_chat_command_receipt (id, channel_id, chat_key, message_id, command, binding) VALUES ('00000000-0000-0000-0000-000000000031', 'test-channel', 'test-chat', 'platform-message-1', '/new', $1)`, previousGAGroupID)
	assertConstraintViolation(t, err, "channel_chat_command_receipt_channel_id_chat_key_message_id_key")

	var mediaID *string
	if err := db.QueryRow(ctx, `SELECT media_id::text FROM ctx_message_part WHERE id = $1`, previousGAPartID).Scan(&mediaID); err != nil {
		t.Fatalf("read legacy message part: %v", err)
	}
	if mediaID != nil {
		t.Fatalf("legacy message part media_id = %q, want NULL", *mediaID)
	}

	if _, err := db.Exec(ctx, `INSERT INTO webhook (id, user_id, agent_id, name, provider, token_public_id, token_hash, token_last4, created_at, updated_at) VALUES ($1, $2, $3, 'valid', 'test', 'webhook-public-id', 'webhook-hash', '1234', $4, $4)`, previousGAWebhookID, previousGAUserID, previousGAAgentID, previousGATime); err != nil {
		t.Fatalf("insert valid webhook: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO webhook (id, user_id, agent_id, name, provider, token_public_id, token_hash, token_last4) VALUES ('00000000-0000-0000-0000-000000000010', $1, 'missing-agent', 'invalid', 'test', 'other-public-id', 'hash', '1234')`, previousGAUserID)
	assertConstraintViolation(t, err, "webhook_agent_id_fkey")
	_, err = db.Exec(ctx, `INSERT INTO webhook (id, user_id, agent_id, name, provider, token_public_id, token_hash, token_last4) VALUES ('00000000-0000-0000-0000-000000000010', $1, $2, 'duplicate', 'test', 'webhook-public-id', 'hash', '1234')`, previousGAUserID, previousGAAgentID)
	assertConstraintViolation(t, err, "webhook_token_public_id_key")

	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = byte(i)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ctx_media (id, user_id, sha256, mime_type, size_bytes, created_at, updated_at) VALUES ($1, $2, $3, 'text/plain', 1, $4, $4)`, previousGAMediaID, previousGAUserID, hash, previousGATime); err != nil {
		t.Fatalf("insert valid media: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO ctx_media (id, user_id, sha256, mime_type, size_bytes) VALUES ('00000000-0000-0000-0000-000000000010', $1, $2, 'text/plain', 1)`, previousGAUserID, hash)
	assertConstraintViolation(t, err, "ctx_media_user_id_sha256_key")
	_, err = db.Exec(ctx, `INSERT INTO ctx_media (id, user_id, sha256, mime_type, size_bytes) VALUES ('00000000-0000-0000-0000-000000000010', $1, $2, 'text/plain', 1)`, previousGAUserID, hash[:31])
	assertConstraintViolation(t, err, "ctx_media_sha256_check")
	if _, err := db.Exec(ctx, `UPDATE ctx_message_part SET media_id = $1 WHERE id = $2`, previousGAMediaID, previousGAPartID); err != nil {
		t.Fatalf("link message part to media: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM ctx_media WHERE id = $1`, previousGAMediaID); err != nil {
		t.Fatalf("delete linked media: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT media_id::text FROM ctx_message_part WHERE id = $1`, previousGAPartID).Scan(&mediaID); err != nil {
		t.Fatalf("read media-cleared part: %v", err)
	}
	if mediaID != nil {
		t.Fatalf("message part media_id after media delete = %q, want NULL", *mediaID)
	}

	if _, err := db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc, created_at, updated_at) VALUES ($1, $2, 'ciphertext', $3, $3)`, previousGACascadeAgentID, previousGAProviderID, previousGATime); err != nil {
		t.Fatalf("insert valid agent Provider credential: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc) VALUES ($1, $2, 'duplicate-ciphertext')`, previousGACascadeAgentID, previousGAProviderID)
	assertConstraintViolation(t, err, "agent_provider_credential_pkey")
	_, err = db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc) VALUES ($1, $2, '')`, previousGAAgentID, previousGAProviderID)
	assertConstraintViolation(t, err, "agent_provider_credential_api_key_enc_check")
	_, err = db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc) VALUES ('missing-agent', $1, 'ciphertext')`, previousGAProviderID)
	assertConstraintViolation(t, err, "agent_provider_credential_agent_id_fkey")
	_, err = db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc) VALUES ($1, 'missing-provider', 'ciphertext')`, previousGACascadeAgentID)
	assertConstraintViolation(t, err, "agent_provider_credential_provider_id_fkey")
	if _, err := db.Exec(ctx, `DELETE FROM agent WHERE id = $1`, previousGACascadeAgentID); err != nil {
		t.Fatalf("delete credential Agent: %v", err)
	}
	if got := count("credentials after Agent cascade", `SELECT count(*) FROM agent_provider_credential WHERE agent_id = $1`, previousGACascadeAgentID); got != 0 {
		t.Fatalf("credential rows after Agent delete = %d, want 0", got)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc, created_at, updated_at) VALUES ($1, $2, 'provider-ciphertext', $3, $3)`, previousGAAgentID, previousGAProviderID, previousGATime); err != nil {
		t.Fatalf("insert provider-cascade credential: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM provider WHERE id = $1`, previousGAProviderID); err != nil {
		t.Fatalf("delete credential Provider: %v", err)
	}
	if got := count("credentials after Provider cascade", `SELECT count(*) FROM agent_provider_credential WHERE provider_id = $1`, previousGAProviderID); got != 0 {
		t.Fatalf("credential rows after Provider delete = %d, want 0", got)
	}

	// Exercise durable channel guest identity and its active Agent conversation
	// after upgrading the previous GA database.
	if _, err := db.Exec(ctx, `
		INSERT INTO channel (id, name, type, agent_id, enabled, created_at, updated_at)
		VALUES ('previous-ga-discord', 'Previous GA Discord', 'discord', $1, true, $2, $2)
	`, previousGAAgentID, previousGATime); err != nil {
		t.Fatalf("insert Discord channel after previous-GA upgrade: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO channel_guest (id, channel_id, platform, external_id, created_at, updated_at)
		VALUES ($1, 'previous-ga-discord', 'discord', 'previous-ga-user', $2, $2)
	`, previousGAGuestID, previousGATime); err != nil {
		t.Fatalf("insert channel guest after previous-GA upgrade: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (
			id, session_id, channel, kind, agent_id, user_id, guest_id,
			last_active, created_at, updated_at
		) VALUES ($1, 'previous-ga-agent:guest:discord', 'discord', 'chat', $2, $3::text, $3::uuid, $4, $4, $4)
	`, previousGAGuestChatID, previousGAAgentID, previousGAGuestID, previousGATime); err != nil {
		t.Fatalf("insert guest conversation after previous-GA upgrade: %v", err)
	}
	if got := count("channel guest conversation", `
		SELECT count(*)
		FROM ctx_conversation AS conversation
		JOIN channel_guest AS guest ON guest.id = conversation.guest_id
		WHERE conversation.id = $1 AND guest.channel_id = 'previous-ga-discord'
	`, previousGAGuestChatID); got != 1 {
		t.Fatalf("channel guest conversations = %d, want 1", got)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO ctx_conversation (
			id, session_id, channel, kind, agent_id, user_id, guest_id
		) VALUES ('00000000-0000-0000-0000-000000000046', 'previous-ga-agent:guest:duplicate', 'discord', 'chat', $1, $2::text, $2::uuid)
	`, previousGAAgentID, previousGAGuestID)
	assertConstraintViolation(t, err, "idx_one_agent_guest_chat")
	_, err = sqlc.New(db).CreateChannelGuest(ctx, sqlc.CreateChannelGuestParams{
		ID: "00000000-0000-0000-0000-000000000047", ChannelID: "previous-ga-discord",
		Platform: "discord", ExternalID: "over-cap", MaxGuests: 1,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("create channel guest above cap = %v, want no rows", err)
	}

	// Exercise the complete Knowledge snapshot publication relationship after
	// upgrading the previous GA database, rather than checking table names only.
	if _, err := db.Exec(ctx, `
		INSERT INTO knowledge_file (
			id, scope, user_id, agent_id, file_name, media_type,
			size_bytes, raw_sha256, status
		) VALUES ($1, 'user_agent', $2, $3, 'previous-ga.txt', 'text/plain', 1, $4, 'ready')
	`, previousGAKnowledgeFile, previousGAUserID, previousGAAgentID, hash); err != nil {
		t.Fatalf("insert knowledge file after previous-GA upgrade: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO knowledge_chunk_set (
			id, file_id, derivation_key, processor_key, raw_sha256,
			status, chunk_count, content_digest, completed_at
		) VALUES ($1, $2, 'previous-ga-derivation', 'previous-ga-processor', $3, 'ready', 1, $3, $4)
	`, previousGAChunkSet, previousGAKnowledgeFile, hash, previousGATime); err != nil {
		t.Fatalf("insert knowledge ChunkSet after previous-GA upgrade: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO knowledge_chunk (
			id, chunk_set_id, ordinal, content, content_sha256
		) VALUES ($1, $2, 0, 'previous GA knowledge', $3)
	`, previousGAChunk, previousGAChunkSet, hash); err != nil {
		t.Fatalf("insert knowledge chunk after previous-GA upgrade: %v", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE knowledge_file SET active_chunk_set_id = $1 WHERE id = $2
	`, previousGAChunkSet, previousGAKnowledgeFile); err != nil {
		t.Fatalf("publish knowledge ChunkSet after previous-GA upgrade: %v", err)
	}
	if got := count("published knowledge chunks", `
		SELECT count(*)
		FROM knowledge_file AS file
		JOIN knowledge_chunk_set AS chunk_set ON chunk_set.id = file.active_chunk_set_id
		JOIN knowledge_chunk AS chunk ON chunk.chunk_set_id = chunk_set.id
		WHERE file.id = $1
	`, previousGAKnowledgeFile); got != 1 {
		t.Fatalf("published knowledge chunks = %d, want 1", got)
	}

	if _, err := db.Exec(ctx, `UPDATE channel_guest SET updated_at = now() - interval '31 days' WHERE id = $1`, previousGAGuestID); err != nil {
		t.Fatalf("age guest activity: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel SET config = '{"guest_retention_days":365}' WHERE id = 'previous-ga-discord'`); err != nil {
		t.Fatalf("configure guest retention: %v", err)
	}
	queries := sqlc.New(db)
	deleted, err := queries.PurgeExpiredChannelGuest(ctx)
	if err != nil {
		t.Fatalf("purge expired channel guests: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("guest retention purge deleted %d guests before configured retention, want 0", deleted)
	}
	if _, err := db.Exec(ctx, `UPDATE channel SET config = '{' WHERE id = 'previous-ga-discord'`); err != nil {
		t.Fatalf("set malformed channel config: %v", err)
	}
	deleted, err = queries.PurgeExpiredChannelGuest(ctx)
	if err != nil {
		t.Fatalf("purge expired channel guests with malformed config: %v", err)
	}
	if deleted != 1 || count("retained expired guest conversations", `SELECT count(*) FROM ctx_conversation WHERE guest_id = $1`, previousGAGuestID) != 0 {
		t.Fatalf("guest retention purge deleted %d guests without cascading conversations, want 1 guest and 0 conversations", deleted)
	}

	var latest int64
	if err := db.QueryRow(ctx, `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&latest); err != nil {
		t.Fatalf("read goose migration ledger: %v", err)
	}
	if latest != currentMigrationVersion {
		t.Fatalf("latest goose migration = %d, want %d", latest, currentMigrationVersion)
	}
}

func assertConstraintViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("constraint %s: got error %v, want PostgreSQL constraint violation", constraint, err)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("constraint violation = %q (%s), want %q", pgErr.ConstraintName, pgErr.Code, constraint)
	}
}
