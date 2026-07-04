package credential

import (
	"strings"
	"testing"
)

func TestMintOpaqueFormatAndChecksum(t *testing.T) {
	m, err := MintOpaque(KindPAT)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !strings.HasPrefix(m.Plaintext, PATPrefix) {
		t.Fatalf("plaintext missing family prefix: %q", m.Plaintext)
	}
	if m.Last4 != m.Plaintext[len(m.Plaintext)-4:] {
		t.Fatal("last4 must be the plaintext suffix")
	}

	publicID, secret, err := parseOpaqueToken(PATPrefix, m.Plaintext)
	if err != nil {
		t.Fatalf("parse minted token: %v", err)
	}
	if publicID != m.PublicID {
		t.Fatalf("public id mismatch: %q vs %q", publicID, m.PublicID)
	}
	if hashSecret(secret) != m.TokenHash {
		t.Fatal("hash of parsed secret must equal stored token hash")
	}

	// Flip the last checksum char: parsing must fail (leak detection integrity).
	bad := m.Plaintext[:len(m.Plaintext)-1]
	if last := m.Plaintext[len(m.Plaintext)-1]; last == 'a' {
		bad += "b"
	} else {
		bad += "a"
	}
	if _, _, err := parseOpaqueToken(PATPrefix, bad); err == nil {
		t.Fatal("checksum tampering must be rejected")
	}
}

func TestMintOpaqueRejectsNonOpaqueKinds(t *testing.T) {
	for _, k := range []Kind{KindScoped, Kind("legacy_stella_token"), Kind("bogus")} {
		if _, err := MintOpaque(k); err == nil {
			t.Fatalf("MintOpaque must not mint kind %q", k)
		}
	}
}

func TestMintOpaqueUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		m, err := MintOpaque(KindPAT)
		if err != nil {
			t.Fatal(err)
		}
		if seen[m.PublicID] {
			t.Fatal("public id collision")
		}
		seen[m.PublicID] = true
	}
}
