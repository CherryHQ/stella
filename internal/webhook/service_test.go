package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type memoryStore struct {
	mu   sync.Mutex
	rows map[string]credentialRecord
}

func (s *memoryStore) Create(_ context.Context, row credentialRecord) (credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.ID] = row
	return row, nil
}

func (s *memoryStore) Get(_ context.Context, id, user string) (credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.UserID != user {
		return credentialRecord{}, ErrNotFound
	}
	return row, nil
}

func (s *memoryStore) List(_ context.Context, user string, _, _ int32) ([]credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []credentialRecord
	for _, row := range s.rows {
		if row.UserID == user {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *memoryStore) Update(_ context.Context, req UpdateRequest) (credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.rows[req.ID]
	if !ok || current.UserID != req.UserID {
		return credentialRecord{}, ErrNotFound
	}
	if req.Name != nil {
		current.Name = *req.Name
	}
	if req.AgentID != nil {
		current.AgentID = *req.AgentID
	}
	if req.IsEnabled != nil {
		current.IsEnabled = *req.IsEnabled
	}
	if req.WaitTimeoutSeconds != nil {
		current.WaitTimeoutSeconds = *req.WaitTimeoutSeconds
	}
	if req.MaxRunTimeoutSeconds != nil {
		current.MaxRunTimeoutSeconds = *req.MaxRunTimeoutSeconds
	}
	s.rows[req.ID] = current
	return current, nil
}

func (s *memoryStore) Rotate(_ context.Context, id, user, etag string, next credentialRecord) (credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.rows[id]
	if !ok || current.UserID != user {
		return credentialRecord{}, ErrNotFound
	}
	if current.ETag() != etag {
		return credentialRecord{}, ErrStaleETag
	}
	current.TokenPublicID, current.TokenHash, current.TokenLast4, current.Revision = next.TokenPublicID, next.TokenHash, next.TokenLast4, current.Revision+1
	s.rows[id] = current
	return current, nil
}

func (s *memoryStore) Delete(_ context.Context, id, user string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.UserID != user {
		return 0, nil
	}
	delete(s.rows, id)
	return 1, nil
}

func (s *memoryStore) ResolveByPublicID(_ context.Context, public string) (credentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.TokenPublicID == public {
			return row, nil
		}
	}
	return credentialRecord{}, ErrNotFound
}

func (s *memoryStore) ResolveAdmitted(ctx context.Context, public string) (credentialRecord, error) {
	row, err := s.ResolveByPublicID(ctx, public)
	if err != nil || !row.IsEnabled {
		return credentialRecord{}, ErrNotFound
	}
	return row, nil
}

type activeUsers struct{}

func (activeUsers) IsActive(context.Context, string) (bool, error) { return true, nil }

type allowedAccess struct{}

func (allowedAccess) CanUseUser(context.Context, string, string) (bool, error) { return true, nil }

func newMemoryService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(Config{Store: &memoryStore{rows: map[string]credentialRecord{}}, Users: activeUsers{}, Access: allowedAccess{}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestEncodeETagBindsCredentialAndRevision(t *testing.T) {
	base := EncodeETag("pub-a", 1)
	if base == "" || base != EncodeETag("pub-a", 1) {
		t.Fatal("etag must be non-empty and deterministic")
	}
	if base == EncodeETag("pub-a", 2) || base == EncodeETag("pub-b", 1) {
		t.Fatal("etag must bind both credential and revision")
	}
	if strings.Contains(base, "pub-a") {
		t.Fatalf("etag leaks public id: %q", base)
	}
}

func TestCreateValidatesResource(t *testing.T) {
	valid := CreateRequest{UserID: uuid.NewString(), Name: "deploy", AgentID: "agent", Provider: ProviderGeneric, IsEnabled: true, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300}
	tests := []struct {
		name string
		edit func(*CreateRequest)
		want error
	}{
		{"user", func(r *CreateRequest) { r.UserID = "invalid" }, ErrInvalidUserID},
		{"name", func(r *CreateRequest) { r.Name = "  " }, ErrInvalidName},
		{"agent", func(r *CreateRequest) { r.AgentID = "" }, ErrInvalidAgentID},
		{"provider", func(r *CreateRequest) { r.Provider = Provider("github") }, ErrInvalidProvider},
		{"wait timeout", func(r *CreateRequest) { r.WaitTimeoutSeconds = 0 }, ErrInvalidTimeout},
		{"run timeout", func(r *CreateRequest) { r.MaxRunTimeoutSeconds = RunTimeoutCeilingSeconds + 1 }, ErrInvalidTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.edit(&req)
			if _, err := newMemoryService(t).Create(context.Background(), req); !errors.Is(err, tc.want) {
				t.Fatalf("Create error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRotateRejectsEmptyETag(t *testing.T) {
	if _, err := newMemoryService(t).Rotate(context.Background(), uuid.NewString(), uuid.NewString(), ""); !errors.Is(err, ErrInvalidETag) {
		t.Fatalf("Rotate error = %v, want ErrInvalidETag", err)
	}
}

func TestResolveCandidateRejectsMalformedTamperedAndRedacts(t *testing.T) {
	svc := newMemoryService(t)
	for _, raw := range []string{"", "stella_pat_abc", TokenPrefix + "garbage"} {
		if _, err := svc.ResolveCandidate(context.Background(), raw); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveCandidate(%q) = %v, want ErrNotFound", raw, err)
		}
	}
	created, err := svc.Create(context.Background(), CreateRequest{UserID: uuid.NewString(), Name: "deploy", AgentID: "agent", Provider: ProviderGeneric, IsEnabled: true, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	cand, err := svc.ResolveCandidate(context.Background(), created.Capability)
	if err != nil {
		t.Fatal(err)
	}
	if cand.WebhookID != created.Webhook.ID {
		t.Fatalf("candidate webhook = %q, want %q", cand.WebhookID, created.Webhook.ID)
	}
	for _, rendered := range []string{cand.String(), fmt.Sprintf("%+v", cand)} {
		if strings.Contains(rendered, cand.secret) || strings.Contains(rendered, cand.publicID) {
			t.Fatalf("candidate formatting leaks verifier: %q", rendered)
		}
	}
	replacement := byte('A')
	if created.Capability[len(created.Capability)-1] == replacement {
		replacement = 'B'
	}
	tampered := created.Capability[:len(created.Capability)-1] + string(replacement)
	if _, err := svc.ResolveCandidate(context.Background(), tampered); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tampered capability = %v, want ErrNotFound", err)
	}
}

func TestResourceLifecycleIsOwnerScopedAndRotatesCAS(t *testing.T) {
	userA, userB := uuid.NewString(), uuid.NewString()
	svc, err := NewService(Config{Store: &memoryStore{rows: map[string]credentialRecord{}}, Users: activeUsers{}, Access: allowedAccess{}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(context.Background(), CreateRequest{UserID: userA, Name: "deploy", AgentID: "agent", Provider: ProviderGeneric, IsEnabled: true, WaitTimeoutSeconds: 60, MaxRunTimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if created.Webhook.ID == "" || created.Capability == "" {
		t.Fatal("create did not mint id and capability")
	}
	if _, err := svc.Get(context.Background(), userB, created.Webhook.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign Get = %v, want opaque not found", err)
	}
	if deleted, err := svc.Delete(context.Background(), userB, created.Webhook.ID); err != nil || deleted {
		t.Fatalf("foreign Delete = (%v, %v), want (false, nil)", deleted, err)
	}
	rotated, err := svc.Rotate(context.Background(), userA, created.Webhook.ID, created.Webhook.ETag())
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Capability == created.Capability || rotated.Webhook.ETag() == created.Webhook.ETag() {
		t.Fatal("rotate did not replace credential")
	}
	if _, err := svc.Rotate(context.Background(), userA, created.Webhook.ID, created.Webhook.ETag()); !errors.Is(err, ErrStaleETag) {
		t.Fatalf("stale rotate = %v, want ErrStaleETag", err)
	}
	if deleted, err := svc.Delete(context.Background(), userA, created.Webhook.ID); err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
}
