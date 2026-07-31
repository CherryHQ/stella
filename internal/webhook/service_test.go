package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// testUsers and testAccess are the issuance-validation ports as simple decisions
// so unit tests exercise the service without a database or the agent PEP.
type testUsers struct{ active bool }

func (u testUsers) IsActive(context.Context, string) (bool, error) { return u.active, nil }

type testAccess struct{ allowed bool }

func (a testAccess) CanUseOwner(context.Context, string, string) (bool, error) {
	return a.allowed, nil
}

func newTestService(t *testing.T, store Store) *Service {
	t.Helper()
	svc, err := NewService(Config{Store: store, Users: testUsers{true}, Access: testAccess{true}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestEncodeETagBindsPublicIDAndRevision(t *testing.T) {
	base := EncodeETag("pub-a", 1)
	if base == "" {
		t.Fatal("EncodeETag returned empty")
	}
	if base != EncodeETag("pub-a", 1) {
		t.Fatal("EncodeETag must be deterministic for the same inputs")
	}
	// A different revision or a different credential id yields a different etag,
	// so a stale etag cannot match after rotate or revoke+recreate.
	if base == EncodeETag("pub-a", 2) {
		t.Fatal("etag must change with revision")
	}
	if base == EncodeETag("pub-b", 1) {
		t.Fatal("etag must change with token_public_id (ABA / cross-channel guard)")
	}
	// The opaque etag must not embed the raw public id.
	if strings.Contains(base, "pub-a") {
		t.Fatalf("etag leaks token_public_id: %q", base)
	}
}

func TestIssueValidatesInput(t *testing.T) {
	svc := newTestService(t, &memStore{})
	valid := uuid.Must(uuid.NewV7()).String()
	cases := []struct {
		name string
		req  IssueRequest
		want error
	}{
		{"empty channel", IssueRequest{OwnerUserID: valid, Provider: ProviderGeneric}, ErrInvalidChannelID},
		{"bad owner", IssueRequest{ChannelID: "c", OwnerUserID: "nope", Provider: ProviderGeneric}, ErrInvalidOwnerUserID},
		{"bad provider", IssueRequest{ChannelID: "c", OwnerUserID: valid, Provider: Provider("github")}, ErrInvalidProvider},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Issue(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("Issue error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRotateRejectsEmptyETag(t *testing.T) {
	svc := newTestService(t, &memStore{})
	if _, err := svc.Rotate(context.Background(), "c", ""); !errors.Is(err, ErrInvalidETag) {
		t.Fatalf("Rotate(\"\") error = %v, want ErrInvalidETag", err)
	}
}

func TestResolveCandidateRejectsForeignAndMalformedTokens(t *testing.T) {
	svc := newTestService(t, &memStore{})
	for _, raw := range []string{"", "stella_pat_abc", TokenPrefix + "garbage"} {
		if _, err := svc.ResolveCandidate(context.Background(), raw); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveCandidate(%q) error = %v, want ErrNotFound", raw, err)
		}
	}
}

func TestResolveCandidateRejectsWrongSecretAndRedacts(t *testing.T) {
	store := &memStore{binding: ChannelBinding{Type: "webhook", AgentID: "a", AgentEnabled: true}}
	svc := newTestService(t, store)
	owner := uuid.Must(uuid.NewV7()).String()
	issued, err := svc.Issue(context.Background(), IssueRequest{ChannelID: "c", OwnerUserID: owner, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// The real capability resolves to a candidate; a token sharing the public id
	// but a different secret must not (constant-time mismatch → ErrNotFound).
	cand, err := svc.ResolveCandidate(context.Background(), issued.Capability)
	if err != nil {
		t.Fatalf("ResolveCandidate(issued): %v", err)
	}
	if cand.EndpointID != "c" {
		t.Fatalf("candidate endpoint = %q, want c", cand.EndpointID)
	}
	// The candidate must not leak the secret via formatting or logging.
	if s := cand.String(); strings.Contains(s, cand.secret) || strings.Contains(s, cand.publicID) {
		t.Fatalf("candidate String leaks verifier material: %q", s)
	}
	if s := fmt.Sprintf("%+v", cand); strings.Contains(s, cand.secret) {
		t.Fatalf("candidate %%+v leaks secret: %q", s)
	}
	tampered := issued.Capability[:len(issued.Capability)-4] + "0000"
	if _, err := svc.ResolveCandidate(context.Background(), tampered); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveCandidate(tampered) error = %v, want ErrNotFound", err)
	}
	if strings.Contains(issued.Capability, store.rec.TokenHash) {
		t.Fatal("capability plaintext leaked into stored hash")
	}
}

// memStore is an in-memory Store for unit tests. It preserves the compare-and-set
// revision semantics the Postgres store enforces with a row lock. active toggles
// whether ResolveEndpoint (the deep admission read) returns the endpoint.
type memStore struct {
	binding ChannelBinding
	rec     *endpointRecord
	active  bool
}

func (m *memStore) BindEndpoint(ctx context.Context, channelID string, build func(context.Context, ChannelBinding) (endpointRecord, error)) (endpointRecord, error) {
	if m.rec != nil {
		return endpointRecord{}, ErrEndpointExists
	}
	binding := m.binding
	binding.ChannelID = channelID
	rec, err := build(ctx, binding)
	if err != nil {
		return endpointRecord{}, err
	}
	rec.ChannelID = channelID
	rec.Revision = 1
	m.rec = &rec
	return rec, nil
}

func (m *memStore) ObserveBinding(_ context.Context, channelID string) (ChannelBinding, error) {
	b := m.binding
	b.ChannelID = channelID
	return b, nil
}

func (m *memStore) ResolveEndpoint(_ context.Context, publicID string) (resolvedRecord, error) {
	if m.rec == nil || m.rec.TokenPublicID != publicID {
		return resolvedRecord{}, ErrNotFound
	}
	return resolvedRecord{
		endpointRecord: *m.rec,
		AgentID:        m.binding.AgentID,
		ChannelEnabled: m.active,
		OwnerActive:    m.active,
		AgentEnabled:   m.active,
	}, nil
}

func (m *memStore) GetEndpointByChannel(context.Context, string) (endpointRecord, error) {
	if m.rec == nil {
		return endpointRecord{}, ErrNotFound
	}
	return *m.rec, nil
}

func (m *memStore) ResolveByPublicID(_ context.Context, publicID string) (endpointRecord, error) {
	if m.rec == nil || m.rec.TokenPublicID != publicID {
		return endpointRecord{}, ErrNotFound
	}
	return *m.rec, nil
}

func (m *memStore) RotateEndpoint(_ context.Context, channelID string, expectedETag string, next endpointRecord) (endpointRecord, error) {
	if m.rec == nil {
		return endpointRecord{}, ErrNotFound
	}
	if EncodeETag(m.rec.TokenPublicID, m.rec.Revision) != expectedETag {
		return endpointRecord{}, ErrStaleETag
	}
	updated := *m.rec
	updated.TokenPublicID = next.TokenPublicID
	updated.TokenHash = next.TokenHash
	updated.TokenLast4 = next.TokenLast4
	updated.Revision++
	m.rec = &updated
	return updated, nil
}

func (m *memStore) DeleteEndpoint(context.Context, string) (int64, error) {
	if m.rec == nil {
		return 0, nil
	}
	m.rec = nil
	return 1, nil
}
