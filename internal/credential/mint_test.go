package credential

import (
	"strings"
	"testing"
)

func TestMintOpaqueFormatAndChecksum(t *testing.T) {
	for _, tc := range []struct {
		kind   Kind
		prefix string
	}{
		{KindPAT, PATPrefix},
		{KindProvisioning, ProvisioningPrefix},
	} {
		m, err := MintOpaque(tc.kind)
		if err != nil {
			t.Fatalf("mint %s: %v", tc.kind, err)
		}
		if !strings.HasPrefix(m.Plaintext, tc.prefix) {
			t.Fatalf("%s plaintext missing family prefix: %q", tc.kind, m.Plaintext)
		}
		if m.Last4 != m.Plaintext[len(m.Plaintext)-4:] {
			t.Fatal("last4 must be the plaintext suffix")
		}

		publicID, secret, err := parseOpaqueToken(tc.prefix, m.Plaintext)
		if err != nil {
			t.Fatalf("parse minted %s token: %v", tc.kind, err)
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
		if _, _, err := parseOpaqueToken(tc.prefix, bad); err == nil {
			t.Fatal("checksum tampering must be rejected")
		}
	}
}

func TestMintOpaqueRejectsNonOpaqueKinds(t *testing.T) {
	for _, k := range []Kind{Kind("scoped"), Kind("legacy_stella_token"), Kind("bogus")} {
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
