package providercred_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
)

// fakeCipher is a reversible stand-in for vault.Service. It can be forced to fail
// encryption of a chosen plaintext to exercise the failure paths.
type fakeCipher struct {
	failOnPlaintext string
}

func (c fakeCipher) EncryptSystem(plaintext string) (string, error) {
	if c.failOnPlaintext != "" && plaintext == c.failOnPlaintext {
		return "", fmt.Errorf("forced encrypt failure for %s", plaintext)
	}
	return "enc:" + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (c fakeCipher) DecryptSystem(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "enc:"))
	return string(raw), err
}

// fakeStore records what the Store port receives so tests can assert that only
// ciphertext crosses the boundary and that failed encryption never reaches it.
type fakeStore struct {
	upserts        []providercred.Encrypted
	createCalls    int
	createdCreds   []providercred.Encrypted
	createdAgentID string
}

func (s *fakeStore) ListAgentProviderCredentials(context.Context, string) ([]providercred.Metadata, error) {
	return nil, nil
}

func (s *fakeStore) GetAgentProviderCredential(context.Context, string, string) (providercred.Encrypted, bool, error) {
	return providercred.Encrypted{}, false, nil
}

func (s *fakeStore) UpsertAgentProviderCredential(_ context.Context, _ string, cred providercred.Encrypted) (providercred.Metadata, error) {
	s.upserts = append(s.upserts, cred)
	return providercred.Metadata{ProviderID: cred.ProviderID, HasAPIKey: true}, nil
}

func (s *fakeStore) DeleteAgentProviderCredential(context.Context, string, string) error {
	return nil
}

func (s *fakeStore) CreateAgentWithCredentials(_ context.Context, a config.Agent, creds []providercred.Encrypted) error {
	s.createCalls++
	s.createdAgentID = a.ID
	s.createdCreds = creds
	return nil
}

func TestServiceEncryptsBeforePersist(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{})

	if _, err := svc.Set(context.Background(), "a1", providercred.Input{ProviderID: "openai", APIKey: "sk-secret"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.APIKeyEnc == "" || strings.Contains(got.APIKeyEnc, "sk-secret") {
		t.Errorf("plaintext reached the store: %q", got.APIKeyEnc)
	}
}

func TestServiceSetRejectsEmptyKey(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{})

	_, err := svc.Set(context.Background(), "a1", providercred.Input{ProviderID: "openai", APIKey: ""})
	if !errors.Is(err, providercred.ErrEmptyAPIKey) {
		t.Fatalf("err = %v, want ErrEmptyAPIKey", err)
	}
	if len(store.upserts) != 0 {
		t.Errorf("store was written despite empty key: %+v", store.upserts)
	}
}

func TestServiceSetRejectsEmptyProviderID(t *testing.T) {
	svc := providercred.NewService(&fakeStore{}, fakeCipher{})
	_, err := svc.Set(context.Background(), "a1", providercred.Input{ProviderID: "", APIKey: "k"})
	if !errors.Is(err, providercred.ErrEmptyProviderID) {
		t.Fatalf("err = %v, want ErrEmptyProviderID", err)
	}
}

func TestServiceSetRotationPreservesOldOnEncryptFailure(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{failOnPlaintext: "new-key"})

	_, err := svc.Set(context.Background(), "a1", providercred.Input{ProviderID: "openai", APIKey: "new-key"})
	if err == nil {
		t.Fatal("Set: expected encrypt failure")
	}
	if !errors.Is(err, providercred.ErrUnavailable) || strings.Contains(err.Error(), "new-key") {
		t.Fatalf("Set returned unsafe encryption error: %v", err)
	}
	// The store is never touched, so any previously stored ciphertext stands.
	if len(store.upserts) != 0 {
		t.Errorf("store was written despite encrypt failure: %+v", store.upserts)
	}
}

func TestServiceCreateRejectsDuplicateProvider(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{})

	err := svc.CreateAgentWithCredentials(context.Background(), config.Agent{ID: "a1"}, []providercred.Input{
		{ProviderID: "openai", APIKey: "k1"},
		{ProviderID: "openai", APIKey: "k2"},
	})
	if !errors.Is(err, providercred.ErrDuplicateProvider) {
		t.Fatalf("err = %v, want ErrDuplicateProvider", err)
	}
	if store.createCalls != 0 {
		t.Errorf("store.Create called %d times despite duplicate", store.createCalls)
	}
}

func TestServiceRejectsBoundedCredentialInputs(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{})

	tooMany := make([]providercred.Input, providercred.MaxCredentialsPerCreate+1)
	for i := range tooMany {
		tooMany[i] = providercred.Input{ProviderID: fmt.Sprintf("provider-%d", i), APIKey: "k"}
	}
	if err := svc.CreateAgentWithCredentials(context.Background(), config.Agent{ID: "a1"}, tooMany); !errors.Is(err, providercred.ErrTooManyCredentials) {
		t.Fatalf("too many credentials err = %v", err)
	}
	if _, err := svc.Set(context.Background(), "a1", providercred.Input{
		ProviderID: strings.Repeat("p", providercred.MaxProviderIDLength+1), APIKey: "k",
	}); !errors.Is(err, providercred.ErrProviderIDTooLong) {
		t.Fatalf("long provider id err = %v", err)
	}
	if _, err := svc.Set(context.Background(), "a1", providercred.Input{
		ProviderID: "openai", APIKey: strings.Repeat("k", providercred.MaxAPIKeyLength+1),
	}); !errors.Is(err, providercred.ErrAPIKeyTooLong) {
		t.Fatalf("long api key err = %v", err)
	}
}

func TestServiceCreateEncryptFailureSkipsStore(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{failOnPlaintext: "k2"})

	err := svc.CreateAgentWithCredentials(context.Background(), config.Agent{ID: "a1"}, []providercred.Input{
		{ProviderID: "openai", APIKey: "k1"},
		{ProviderID: "anthropic", APIKey: "k2"},
	})
	if err == nil {
		t.Fatal("expected encrypt failure")
	}
	if store.createCalls != 0 {
		t.Errorf("store.Create called %d times despite encrypt failure — a partial Agent could be created", store.createCalls)
	}
}

func TestServiceCreateEncryptsAllInputs(t *testing.T) {
	store := &fakeStore{}
	svc := providercred.NewService(store, fakeCipher{})

	err := svc.CreateAgentWithCredentials(context.Background(), config.Agent{ID: "a1"}, []providercred.Input{
		{ProviderID: "openai", APIKey: "k1"},
		{ProviderID: "anthropic", APIKey: "k2"},
	})
	if err != nil {
		t.Fatalf("CreateAgentWithCredentials: %v", err)
	}
	if store.createCalls != 1 || store.createdAgentID != "a1" {
		t.Fatalf("create calls = %d agent = %q", store.createCalls, store.createdAgentID)
	}
	want := map[string]string{
		"openai":    "enc:" + base64.StdEncoding.EncodeToString([]byte("k1")),
		"anthropic": "enc:" + base64.StdEncoding.EncodeToString([]byte("k2")),
	}
	for _, c := range store.createdCreds {
		if want[c.ProviderID] != c.APIKeyEnc {
			t.Errorf("cred %q = %q, want %q", c.ProviderID, c.APIKeyEnc, want[c.ProviderID])
		}
	}
}

func TestSecretTypesDoNotMarshalKeyMaterial(t *testing.T) {
	const secret = "must-not-serialize"
	values := []any{
		providercred.Input{ProviderID: "openai", APIKey: secret},
		providercred.Encrypted{ProviderID: "openai", APIKeyEnc: secret},
	}
	for _, value := range values {
		blob, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", value, err)
		}
		if strings.Contains(string(blob), secret) || strings.Contains(string(blob), "APIKey") {
			t.Fatalf("Marshal(%T) leaked key material: %s", value, blob)
		}
		for _, formatted := range []string{
			fmt.Sprintf("%v", value),
			fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value),
		} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("fmt(%T) leaked key material: %s", value, formatted)
			}
		}
	}
}

func TestServiceFailsClosedWhenDependenciesUnavailable(t *testing.T) {
	ctx := context.Background()
	withoutCipher := providercred.NewService(&fakeStore{}, nil)
	if _, err := withoutCipher.Set(ctx, "a1", providercred.Input{ProviderID: "openai", APIKey: "k"}); !errors.Is(err, providercred.ErrUnavailable) {
		t.Fatalf("Set err = %v, want ErrUnavailable", err)
	}
	withoutStore := providercred.NewService(nil, fakeCipher{})
	if err := withoutStore.CreateAgentWithCredentials(ctx, config.Agent{ID: "a1"}, nil); !errors.Is(err, providercred.ErrUnavailable) {
		t.Fatalf("Create err = %v, want ErrUnavailable", err)
	}
}
