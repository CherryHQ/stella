package store_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent/providercred"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/store"
)

// b64Cipher is a reversible test cipher whose ciphertext does not embed the
// plaintext, so "ciphertext, never plaintext" assertions against raw SQL are
// meaningful. failOn forces an encryption failure for one plaintext.
type b64Cipher struct{ failOn string }

func (c b64Cipher) EncryptSystem(plaintext string) (string, error) {
	if c.failOn != "" && plaintext == c.failOn {
		return "", errors.New("forced encrypt failure")
	}
	return "enc:" + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (c b64Cipher) DecryptSystem(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "enc:"))
	return string(raw), err
}

func createTestProvider(t *testing.T, s *store.DBStore, id string) {
	t.Helper()
	if err := s.CreateProvider(context.Background(), config.Provider{
		ID:      id,
		Type:    id,
		Name:    id,
		Enabled: true,
		APIKey:  "", // global key intentionally empty: overrides must still work
		BaseURL: "https://" + id + ".example.com",
	}); err != nil {
		t.Fatalf("CreateProvider %q: %v", id, err)
	}
}

func newCredAgent(id string) config.Agent {
	return config.Agent{ID: id, Name: id, Workspace: "/tmp/" + id, Enabled: true}
}

func countAgentCredentials(t *testing.T, db *pgxpool.Pool, agentID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		"SELECT count(*) FROM agent_provider_credential WHERE agent_id = $1", agentID).Scan(&n); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	return n
}

func TestCreateAgentWithCredentialsAtomic(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	createTestProvider(t, s, "anthropic")

	svc := providercred.NewService(s, b64Cipher{})
	agent := newCredAgent("enterprise")
	if err := svc.CreateAgentWithCredentials(ctx, agent, []providercred.Input{
		{ProviderID: "openai", APIKey: "sk-openai-plaintext"},
		{ProviderID: "anthropic", APIKey: "sk-anthropic-plaintext"},
	}); err != nil {
		t.Fatalf("CreateAgentWithCredentials: %v", err)
	}

	if _, err := s.GetAgent(ctx, "enterprise"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got := countAgentCredentials(t, db, "enterprise"); got != 2 {
		t.Fatalf("credential rows = %d, want 2", got)
	}

	// Metadata is secret-free.
	metas, err := s.ListAgentProviderCredentials(ctx, "enterprise")
	if err != nil {
		t.Fatalf("ListAgentProviderCredentials: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("metadata len = %d, want 2", len(metas))
	}
	for _, m := range metas {
		if !m.HasAPIKey {
			t.Errorf("meta %q HasAPIKey = false", m.ProviderID)
		}
	}

	// Ciphertext round-trips and the plaintext never appears in the column.
	enc, found, err := s.GetAgentProviderCredential(ctx, "enterprise", "openai")
	if err != nil || !found {
		t.Fatalf("GetAgentProviderCredential: found=%v err=%v", found, err)
	}
	if enc.APIKeyEnc == "sk-openai-plaintext" {
		t.Fatal("stored value is plaintext, not ciphertext")
	}
	if back, _ := (b64Cipher{}).DecryptSystem(enc.APIKeyEnc); back != "sk-openai-plaintext" {
		t.Fatalf("decrypt round-trip = %q, want plaintext", back)
	}

	rows, err := db.Query(ctx, "SELECT api_key_enc FROM agent_provider_credential WHERE agent_id = $1", "enterprise")
	if err != nil {
		t.Fatalf("query ciphertext: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, plaintext := range []string{"sk-openai-plaintext", "sk-anthropic-plaintext"} {
			if strings.Contains(col, plaintext) {
				t.Errorf("plaintext %q found in stored column %q", plaintext, col)
			}
		}
	}
}

func TestCreateAgentWithCredentialsChildFailureRollsBack(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")

	// Two rows for the same provider collide on the composite PK; the second
	// insert aborts the transaction.
	err := s.CreateAgentWithCredentials(ctx, newCredAgent("doomed"), []providercred.Encrypted{
		{ProviderID: "openai", APIKeyEnc: "enc:aaa"},
		{ProviderID: "openai", APIKeyEnc: "enc:bbb"},
	})
	if err == nil {
		t.Fatal("expected duplicate-provider insert to fail")
	}
	if _, gerr := s.GetAgent(ctx, "doomed"); gerr == nil {
		t.Fatal("agent row survived a failed composite create")
	}
	if got := countAgentCredentials(t, db, "doomed"); got != 0 {
		t.Fatalf("credential rows = %d, want 0 after rollback", got)
	}
}

func TestCreateAgentWithCredentialsEncryptFailureNoRows(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	createTestProvider(t, s, "anthropic")

	svc := providercred.NewService(s, b64Cipher{failOn: "boom-key"})
	err := svc.CreateAgentWithCredentials(ctx, newCredAgent("never"), []providercred.Input{
		{ProviderID: "openai", APIKey: "fine"},
		{ProviderID: "anthropic", APIKey: "boom-key"},
	})
	if err == nil {
		t.Fatal("expected encrypt failure")
	}
	if _, gerr := s.GetAgent(ctx, "never"); gerr == nil {
		t.Fatal("agent was created despite encrypt failure")
	}
}

func TestUpsertUniquenessAndRotation(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("rotator")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	meta, err := s.UpsertAgentProviderCredential(ctx, "rotator", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:v1"})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if meta.ProviderID != "openai" || !meta.HasAPIKey || meta.UpdatedAt.IsZero() || meta.UpdatedAt.Location() != time.UTC {
		t.Fatalf("first upsert metadata = %+v", meta)
	}
	if _, err := s.UpsertAgentProviderCredential(ctx, "rotator", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:v2"}); err != nil {
		t.Fatalf("rotation upsert: %v", err)
	}

	if got := countAgentCredentials(t, db, "rotator"); got != 1 {
		t.Fatalf("credential rows = %d, want 1 (upsert, not insert)", got)
	}
	enc, _, err := s.GetAgentProviderCredential(ctx, "rotator", "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if enc.APIKeyEnc != "enc:v2" {
		t.Fatalf("APIKeyEnc = %q, want rotated enc:v2", enc.APIKeyEnc)
	}
}

func TestSetRotationPreservesOldOnEncryptFailure(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("keep-old")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if _, err := providercred.NewService(s, b64Cipher{}).Set(ctx, "keep-old", providercred.Input{ProviderID: "openai", APIKey: "original"}); err != nil {
		t.Fatalf("initial Set: %v", err)
	}

	// A rotation whose encryption fails must leave the original ciphertext intact.
	failing := providercred.NewService(s, b64Cipher{failOn: "rotated"})
	if _, err := failing.Set(ctx, "keep-old", providercred.Input{ProviderID: "openai", APIKey: "rotated"}); err == nil {
		t.Fatal("expected encrypt failure on rotation")
	}

	enc, _, err := s.GetAgentProviderCredential(ctx, "keep-old", "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back, _ := (b64Cipher{}).DecryptSystem(enc.APIKeyEnc); back != "original" {
		t.Fatalf("stored key = %q, want original preserved", back)
	}
}

func TestUpsertWriteFailurePreservesOldCiphertext(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("keep-old-write")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := s.UpsertAgentProviderCredential(ctx, "keep-old-write", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:old"}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	// The CHECK rejects the replacement statement atomically.
	if _, err := s.UpsertAgentProviderCredential(ctx, "keep-old-write", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: ""}); err == nil {
		t.Fatal("expected empty-ciphertext write to fail")
	}
	enc, found, err := s.GetAgentProviderCredential(ctx, "keep-old-write", "openai")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if enc.APIKeyEnc != "enc:old" {
		t.Fatalf("ciphertext = %q, want old value preserved", enc.APIKeyEnc)
	}
}

func TestCredentialCascadeOnAgentDelete(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("gone")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := s.UpsertAgentProviderCredential(ctx, "gone", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteAgent(ctx, "gone"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if got := countAgentCredentials(t, db, "gone"); got != 0 {
		t.Fatalf("credential rows = %d, want 0 after agent delete cascade", got)
	}
}

func TestCredentialCascadeOnProviderDelete(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("survivor")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := s.UpsertAgentProviderCredential(ctx, "survivor", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteProvider(ctx, "openai"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if got := countAgentCredentials(t, db, "survivor"); got != 0 {
		t.Fatalf("credential rows = %d, want 0 after provider delete cascade", got)
	}
	// The Agent itself is untouched by a Provider delete.
	if _, err := s.GetAgent(ctx, "survivor"); err != nil {
		t.Fatalf("agent should survive provider delete: %v", err)
	}
}

func TestDeleteAgentProviderCredentialIdempotent(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")
	if err := s.CreateAgent(ctx, newCredAgent("idem")); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Deleting a non-existent override is a no-op, not an error.
	if err := s.DeleteAgentProviderCredential(ctx, "idem", "openai"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := s.UpsertAgentProviderCredential(ctx, "idem", providercred.Encrypted{ProviderID: "openai", APIKeyEnc: "enc:x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteAgentProviderCredential(ctx, "idem", "openai"); err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	if _, found, err := s.GetAgentProviderCredential(ctx, "idem", "openai"); err != nil || found {
		t.Fatalf("credential still present after delete: found=%v err=%v", found, err)
	}
	if err := s.DeleteAgentProviderCredential(ctx, "idem", "openai"); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
}

func TestAgentProjectionSecretFree(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai")

	const secret = "sk-super-secret-value"
	if err := providercred.NewService(s, b64Cipher{}).CreateAgentWithCredentials(ctx, newCredAgent("projected"), []providercred.Input{
		{ProviderID: "openai", APIKey: secret},
	}); err != nil {
		t.Fatalf("CreateAgentWithCredentials: %v", err)
	}

	agent, err := s.GetAgent(ctx, "projected")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	blob, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	for _, needle := range []string{secret, "api_key_enc", "enc:"} {
		if strings.Contains(string(blob), needle) {
			t.Errorf("agent projection leaked %q: %s", needle, blob)
		}
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if blob, _ := json.Marshal(agents); strings.Contains(string(blob), secret) {
		t.Errorf("ListAgents projection leaked the key: %s", blob)
	}
}
