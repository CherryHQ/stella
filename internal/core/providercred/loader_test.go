package providercred_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/core/providercred"
	"github.com/CherryHQ/stella/internal/platform/config"
)

// fakeBase is a base SnapshotLoader returning a fixed snapshot (deep-copied per
// call so overlay mutation never leaks across cases).
type fakeBase struct {
	snap *config.Snapshot
	err  error
}

func (b fakeBase) Snapshot(context.Context, string) (*config.Snapshot, error) {
	if b.err != nil {
		return nil, b.err
	}
	if b.snap == nil {
		return nil, nil
	}
	cp := *b.snap
	cp.Providers = make(map[string]config.ProviderCreds, len(b.snap.Providers))
	maps.Copy(cp.Providers, b.snap.Providers)
	return &cp, nil
}

// fakeReader serves ciphertext rows by canonical provider ID and records every
// lookup so tests can assert decrypt-once and dormant-row-never-read.
type fakeReader struct {
	rows     map[string]providercred.Encrypted
	err      error
	requests []string
}

func (r *fakeReader) GetAgentProviderCredential(_ context.Context, _, providerID string) (providercred.Encrypted, bool, error) {
	r.requests = append(r.requests, providerID)
	if r.err != nil {
		return providercred.Encrypted{}, false, r.err
	}
	enc, ok := r.rows[providerID]
	return enc, ok, nil
}

// overlayCipher reverses fakeCipher's encoding and fails on a sentinel ciphertext.
type overlayCipher struct{ failOnCiphertext string }

func (c overlayCipher) EncryptSystem(string) (string, error) { return "", nil }
func (c overlayCipher) DecryptSystem(ciphertext string) (string, error) {
	if ciphertext == c.failOnCiphertext {
		return "", errors.New("corrupt ciphertext")
	}
	return strings.TrimPrefix(ciphertext, "enc:"), nil
}

func baseSnapshot() *config.Snapshot {
	return &config.Snapshot{
		Provider: "openai",
		Model:    "openai/gpt",
		APIKey:   "GLOBAL",
		BaseURL:  "https://openai.example",
		Providers: map[string]config.ProviderCreds{
			"openai": {Type: "openai-completions", APIKey: "GLOBAL", BaseURL: "https://openai.example", ProviderID: "openai"},
		},
	}
}

func TestOverlayAppliesAgentOverride(t *testing.T) {
	reader := &fakeReader{rows: map[string]providercred.Encrypted{"openai": {ProviderID: "openai", APIKeyEnc: "enc:AGENT"}}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, overlayCipher{})

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snap.Providers["openai"].APIKey; got != "AGENT" {
		t.Errorf("Providers[openai].APIKey = %q, want AGENT", got)
	}
	if snap.APIKey != "AGENT" {
		t.Errorf("legacy top-level APIKey = %q, want AGENT", snap.APIKey)
	}
	// Everything except the key stays global.
	if c := snap.Providers["openai"]; c.Type != "openai-completions" || c.BaseURL != "https://openai.example" {
		t.Errorf("provider metadata changed: %+v", c)
	}
	if snap.BaseURL != "https://openai.example" {
		t.Errorf("top-level BaseURL changed: %q", snap.BaseURL)
	}
}

func TestOverlayAbsentOverrideUsesGlobal(t *testing.T) {
	reader := &fakeReader{rows: map[string]providercred.Encrypted{}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, overlayCipher{})

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Providers["openai"].APIKey != "GLOBAL" || snap.APIKey != "GLOBAL" {
		t.Errorf("absent override should keep global key, got %q / %q", snap.Providers["openai"].APIKey, snap.APIKey)
	}
}

func TestOverlayEmptyGlobalKeyWithOverride(t *testing.T) {
	base := baseSnapshot()
	base.APIKey = ""
	base.Providers["openai"] = config.ProviderCreds{Type: "openai-completions", APIKey: "", BaseURL: "https://openai.example", ProviderID: "openai"}
	reader := &fakeReader{rows: map[string]providercred.Encrypted{"openai": {ProviderID: "openai", APIKeyEnc: "enc:AGENT"}}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: base}, reader, overlayCipher{})

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Providers["openai"].APIKey != "AGENT" || snap.APIKey != "AGENT" {
		t.Errorf("empty global key + override should yield AGENT, got %q / %q", snap.Providers["openai"].APIKey, snap.APIKey)
	}
}

func TestOverlayReferencedDecryptFailureFailsClosed(t *testing.T) {
	reader := &fakeReader{rows: map[string]providercred.Encrypted{"openai": {ProviderID: "openai", APIKeyEnc: "enc:CORRUPT"}}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, overlayCipher{failOnCiphertext: "enc:CORRUPT"})

	if _, err := loader.Snapshot(context.Background(), "a1"); err == nil {
		t.Fatal("referenced override that will not decrypt must fail closed, not fall back to global")
	}
}

func TestOverlayNilCipherReferencedRowFailsClosed(t *testing.T) {
	reader := &fakeReader{rows: map[string]providercred.Encrypted{"openai": {ProviderID: "openai", APIKeyEnc: "enc:AGENT"}}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, nil)

	_, err := loader.Snapshot(context.Background(), "a1")
	if !errors.Is(err, providercred.ErrUnavailable) {
		t.Fatalf("nil cipher with a referenced override must fail closed with ErrUnavailable, got %v", err)
	}
}

func TestOverlayNilCipherNoRowsAllowsGlobal(t *testing.T) {
	reader := &fakeReader{rows: map[string]providercred.Encrypted{}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, nil)

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("keyless deployment with no overrides must work: %v", err)
	}
	if snap.APIKey != "GLOBAL" {
		t.Errorf("APIKey = %q, want GLOBAL", snap.APIKey)
	}
}

func TestOverlayAliasEntriesShareOneOverride(t *testing.T) {
	// Two map keys (canonical id "prov-1" and its type alias "openai") resolve to
	// the same canonical provider; the default ref is the alias.
	base := &config.Snapshot{
		Provider: "openai",
		Model:    "openai/gpt",
		APIKey:   "GLOBAL",
		BaseURL:  "https://openai.example",
		Providers: map[string]config.ProviderCreds{
			"openai": {Type: "openai-completions", APIKey: "GLOBAL", BaseURL: "https://openai.example", ProviderID: "prov-1"},
			"prov-1": {Type: "openai-completions", APIKey: "GLOBAL", BaseURL: "https://openai.example", ProviderID: "prov-1"},
		},
	}
	reader := &fakeReader{rows: map[string]providercred.Encrypted{"prov-1": {ProviderID: "prov-1", APIKeyEnc: "enc:AGENT"}}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: base}, reader, overlayCipher{})

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Providers["openai"].APIKey != "AGENT" || snap.Providers["prov-1"].APIKey != "AGENT" {
		t.Errorf("both alias and canonical entries should carry the override: %+v", snap.Providers)
	}
	if snap.APIKey != "AGENT" {
		t.Errorf("legacy default key should follow the default provider's override, got %q", snap.APIKey)
	}
	// Decrypt-once: exactly one lookup for the shared canonical id.
	if len(reader.requests) != 1 || reader.requests[0] != "prov-1" {
		t.Errorf("expected one lookup for prov-1, got %v", reader.requests)
	}
}

func TestOverlayDormantCorruptRowIgnored(t *testing.T) {
	// The snapshot references only openai; a corrupt override for the unreferenced
	// "anthropic" provider must never be read, so it cannot break this load.
	reader := &fakeReader{rows: map[string]providercred.Encrypted{
		"openai":    {ProviderID: "openai", APIKeyEnc: "enc:AGENT"},
		"anthropic": {ProviderID: "anthropic", APIKeyEnc: "enc:CORRUPT"},
	}}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, overlayCipher{failOnCiphertext: "enc:CORRUPT"})

	snap, err := loader.Snapshot(context.Background(), "a1")
	if err != nil {
		t.Fatalf("dormant corrupt row must not fail an unrelated provider: %v", err)
	}
	if snap.Providers["openai"].APIKey != "AGENT" {
		t.Errorf("referenced override should still apply, got %q", snap.Providers["openai"].APIKey)
	}
	for _, req := range reader.requests {
		if req == "anthropic" {
			t.Error("dormant provider anthropic must never be looked up")
		}
	}
}

func TestOverlayReaderErrorPropagates(t *testing.T) {
	reader := &fakeReader{err: errors.New("db down")}
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, reader, overlayCipher{})
	if _, err := loader.Snapshot(context.Background(), "a1"); err == nil {
		t.Fatal("store read error must propagate, not be swallowed")
	}
}

func TestOverlayBaseErrorPropagates(t *testing.T) {
	loader := providercred.NewCredentialLoader(fakeBase{err: errors.New("boom")}, &fakeReader{}, overlayCipher{})
	if _, err := loader.Snapshot(context.Background(), "a1"); err == nil {
		t.Fatal("base loader error must propagate")
	}
}

func TestOverlayNilReceiverFailsClosed(t *testing.T) {
	var loader *providercred.CredentialLoader
	if _, err := loader.Snapshot(context.Background(), "a1"); !errors.Is(err, providercred.ErrUnavailable) {
		t.Fatalf("nil loader = %v, want ErrUnavailable (no panic)", err)
	}
}

func TestOverlayMissingCredentialStoreFailsClosed(t *testing.T) {
	loader := providercred.NewCredentialLoader(fakeBase{snap: baseSnapshot()}, nil, overlayCipher{})
	if _, err := loader.Snapshot(context.Background(), "a1"); !errors.Is(err, providercred.ErrUnavailable) {
		t.Fatalf("missing credential store = %v, want ErrUnavailable", err)
	}
}
