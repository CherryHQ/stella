package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/credential"
)

const testOwnerID = "00000000-0000-7000-8000-000000000001"

type memoryStore struct {
	mu        sync.Mutex
	binding   string
	endpoints map[string]endpointRecord
	byPublic  map[string]string
	claims    map[string]bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{binding: "agent-1", endpoints: map[string]endpointRecord{}, byPublic: map[string]string{}, claims: map[string]bool{}}
}

func (s *memoryStore) BindEndpoint(ctx context.Context, channelID string, build func(context.Context, ChannelBinding) (endpointRecord, error)) (endpointRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding == "" {
		return endpointRecord{}, ErrNotFound
	}
	rec, err := build(ctx, ChannelBinding{ChannelID: channelID, Type: "webhook", AgentID: s.binding, AgentEnabled: true})
	if err != nil {
		return endpointRecord{}, err
	}
	now := time.Now().UTC()
	rec.ChannelID, rec.CreatedAt, rec.UpdatedAt = channelID, now, now
	s.endpoints[rec.ID] = rec
	s.byPublic[rec.TokenPublicID] = rec.ID
	return rec, nil
}

func (s *memoryStore) GetEndpoint(_ context.Context, id string) (endpointRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.endpoints[id]
	if !ok {
		return endpointRecord{}, ErrNotFound
	}
	return rec, nil
}

func (s *memoryStore) GetEndpointByChannel(_ context.Context, channel string) (endpointRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.endpoints {
		if rec.ChannelID == channel {
			return rec, nil
		}
	}
	return endpointRecord{}, ErrNotFound
}

func (s *memoryStore) ResolveEndpoint(_ context.Context, public string) (resolvedRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byPublic[public]
	if !ok {
		return resolvedRecord{}, ErrNotFound
	}
	rec := s.endpoints[id]
	return resolvedRecord{endpointRecord: rec, AgentID: s.binding, ChannelEnabled: true, OwnerActive: true, AgentEnabled: true}, nil
}

func (s *memoryStore) RotateEndpoint(_ context.Context, next endpointRecord) (endpointRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.endpoints[next.ID]
	if !ok {
		return endpointRecord{}, ErrNotFound
	}
	delete(s.byPublic, current.TokenPublicID)
	next.Endpoint = current.Endpoint
	next.UpdatedAt = time.Now().UTC()
	next.RotatedAt = &next.UpdatedAt
	s.endpoints[next.ID] = next
	s.byPublic[next.TokenPublicID] = next.ID
	return next, nil
}

func (s *memoryStore) DeleteEndpoint(_ context.Context, id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.endpoints[id]
	if !ok {
		return 0, nil
	}
	delete(s.endpoints, id)
	delete(s.byPublic, rec.TokenPublicID)
	return 1, nil
}

func (s *memoryStore) ClaimDelivery(_ context.Context, endpoint string, provider Provider, delivery string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := endpoint + "/" + string(provider) + "/" + delivery
	if s.claims[key] {
		return false, nil
	}
	s.claims[key] = true
	return true, nil
}

func (s *memoryStore) ReleaseDelivery(_ context.Context, endpoint string, provider Provider, delivery string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := endpoint + "/" + string(provider) + "/" + delivery
	if !s.claims[key] {
		return 0, nil
	}
	delete(s.claims, key)
	return 1, nil
}

type testUsers struct{ active bool }

func (u testUsers) IsActive(context.Context, string) (bool, error) { return u.active, nil }

type testAccess struct{ allowed bool }

func (a testAccess) CanUseOwner(context.Context, string, string) (bool, error) { return a.allowed, nil }

type testCipher struct{}

func (testCipher) EncryptSystem(plaintext string) (string, error) { return "cipher:" + plaintext, nil }
func (testCipher) DecryptSystem(ciphertext string) (string, error) {
	if len(ciphertext) < len("cipher:") {
		return "", errors.New("bad ciphertext")
	}
	return ciphertext[len("cipher:"):], nil
}

func newTestService(t *testing.T, store *memoryStore) *Service {
	t.Helper()
	svc, err := NewService(Config{Store: store, Users: testUsers{true}, Access: testAccess{true}, Cipher: testCipher{}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestMatchesTokenHashUsesOpaqueVerifier(t *testing.T) {
	secret := "random-high-entropy-secret"
	hash := credential.HashSecret(secret)
	if !matchesTokenHash(secret, hash) {
		t.Fatal("matching verifier was rejected")
	}
	if matchesTokenHash("different-secret", hash) {
		t.Fatal("mismatched verifier was accepted")
	}
}

func TestIssueStoresOnlyVerifierAndRotatesCapability(t *testing.T) {
	store := newMemoryStore()
	svc := newTestService(t, store)
	issued, err := svc.Issue(context.Background(), IssueRequest{ChannelID: "channel", OwnerUserID: testOwnerID, Provider: ProviderGitHub, GitHub: GitHubPolicy{Events: []string{"push"}, Repositories: []string{"acme/repo"}}})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Capability == "" || issued.GitHubWebhookSecret == "" {
		t.Fatal("one-time credentials missing")
	}
	rec, err := store.GetEndpoint(context.Background(), issued.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TokenHash == issued.Capability || rec.TokenPublicID == issued.Capability || rec.ProviderSecretCiphertext == issued.GitHubWebhookSecret {
		t.Fatal("plaintext credential persisted")
	}
	if rec.ProviderSecretCiphertext == "" {
		t.Fatal("github secret was not encrypted")
	}
	if _, err := svc.ResolveCapability(context.Background(), issued.Capability); err != nil {
		t.Fatalf("resolve issued capability: %v", err)
	}
	rotated, err := svc.Rotate(context.Background(), issued.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Endpoint.Provider != ProviderGitHub {
		t.Fatalf("provider changed on rotation: %q", rotated.Endpoint.Provider)
	}
	if _, err := svc.ResolveCapability(context.Background(), issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old capability error = %v, want ErrNotFound", err)
	}
	if _, err := svc.ResolveCapability(context.Background(), rotated.Capability); err != nil {
		t.Fatalf("resolve rotated capability: %v", err)
	}
}

func TestResolveRejectsCapabilityWithoutPrefix(t *testing.T) {
	store := newMemoryStore()
	svc := newTestService(t, store)
	issued, err := svc.Issue(context.Background(), IssueRequest{ChannelID: "channel", OwnerUserID: testOwnerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveCapability(context.Background(), strings.TrimPrefix(issued.Capability, TokenPrefix)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stripped-prefix error = %v, want ErrNotFound", err)
	}
}

func TestResolveRejectsInactiveDurableStateAndRevocation(t *testing.T) {
	store := newMemoryStore()
	svc := newTestService(t, store)
	issued, err := svc.Issue(context.Background(), IssueRequest{ChannelID: "channel", OwnerUserID: testOwnerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatal(err)
	}
	public := store.endpoints[issued.Endpoint.ID].TokenPublicID
	store.mu.Lock()
	store.endpoints[issued.Endpoint.ID] = endpointRecord{Endpoint: store.endpoints[issued.Endpoint.ID].Endpoint, TokenPublicID: public, TokenHash: store.endpoints[issued.Endpoint.ID].TokenHash}
	store.mu.Unlock()
	// A malformed state is fail-closed because the resolver sees no active agent.
	store.binding = ""
	if _, err := svc.ResolveCapability(context.Background(), issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive binding error = %v", err)
	}
	store.binding = "agent-1"
	deleted, err := svc.Delete(context.Background(), issued.Endpoint.ID)
	if err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, err := svc.ResolveCapability(context.Background(), issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked capability error = %v", err)
	}
}

func TestGitHubValidationUsesRawBytesAllowlistsAndEnvelope(t *testing.T) {
	svc := newTestService(t, newMemoryStore())
	body := []byte(`{ "repository": { "full_name": "acme/repo" }, "comment": "untrusted" }`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	header := http.Header{
		"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(mac.Sum(nil))},
		"X-GitHub-Event":      {"push"}, "X-GitHub-Delivery": {"delivery-1"},
	}
	inv := Invocation{Endpoint: Endpoint{ID: "endpoint", Provider: ProviderGitHub}, githubSecret: "secret"}
	policy := GitHubPolicy{Events: []string{"push"}, Repositories: []string{"acme/repo"}}
	delivery, err := svc.ValidateGitHub(inv, header, body, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateGitHub(inv, header, append(body, ' '), policy); !errors.Is(err, ErrInvalidGitHubDelivery) {
		t.Fatalf("raw-byte HMAC error = %v", err)
	}
	uppercase := header.Clone()
	uppercase.Set("X-Hub-Signature-256", strings.ToUpper(header.Get("X-Hub-Signature-256")))
	if _, err := svc.ValidateGitHub(inv, uppercase, body, policy); !errors.Is(err, ErrInvalidGitHubDelivery) {
		t.Fatalf("uppercase signature error = %v", err)
	}
	if _, err := svc.ValidateGitHub(inv, http.Header{"X-Hub-Signature-256": header["X-Hub-Signature-256"], "X-GitHub-Event": {"issues"}, "X-GitHub-Delivery": {"delivery-1"}}, body, policy); !errors.Is(err, ErrGitHubDeliveryIgnored) {
		t.Fatalf("event allowlist error = %v", err)
	}
	otherRepo := []byte(`{"repository":{"full_name":"other/repo"}}`)
	otherMAC := hmac.New(sha256.New, []byte("secret"))
	_, _ = otherMAC.Write(otherRepo)
	if _, err := svc.ValidateGitHub(inv, http.Header{
		"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(otherMAC.Sum(nil))},
		"X-GitHub-Event":      {"push"},
		"X-GitHub-Delivery":   {"delivery-other-repo"},
	}, otherRepo, policy); !errors.Is(err, ErrGitHubDeliveryIgnored) {
		t.Fatalf("repository allowlist error = %v", err)
	}
	envelope, err := delivery.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"source":"github","trust":"untrusted_external_data","event":"push","delivery_id":"delivery-1","repository":"acme/repo","payload":{"repository":{"full_name":"acme/repo"},"comment":"untrusted"}}`
	if string(envelope) != want {
		t.Fatalf("envelope = %s\nwant = %s", envelope, want)
	}
}

func TestClaimAndReleaseGitHubDelivery(t *testing.T) {
	svc := newTestService(t, newMemoryStore())
	delivery := GitHubDelivery{endpointID: "endpoint", DeliveryID: "delivery"}
	won, err := svc.ClaimGitHubDelivery(context.Background(), delivery)
	if err != nil || !won {
		t.Fatalf("first claim = %v, %v", won, err)
	}
	won, err = svc.ClaimGitHubDelivery(context.Background(), delivery)
	if err != nil || won {
		t.Fatalf("duplicate claim = %v, %v", won, err)
	}
	released, err := svc.ReleaseGitHubDelivery(context.Background(), delivery)
	if err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	won, err = svc.ClaimGitHubDelivery(context.Background(), delivery)
	if err != nil || !won {
		t.Fatalf("claim after pre-admission release = %v, %v", won, err)
	}
}
