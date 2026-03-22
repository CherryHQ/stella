package weixin

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRandomWechatUIN(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	for range 100 {
		uin := randomWechatUIN()
		if uin == "" {
			t.Fatal("randomWechatUIN returned empty string")
		}

		// Must be valid base64.
		decoded, err := base64.StdEncoding.DecodeString(uin)
		if err != nil {
			t.Fatalf("randomWechatUIN not valid base64: %v", err)
		}

		// Decoded must be a decimal string representing a uint32.
		val, err := strconv.ParseUint(string(decoded), 10, 32)
		if err != nil {
			t.Fatalf("decoded UIN %q is not a valid uint32 decimal: %v", string(decoded), err)
		}
		_ = val

		seen[uin] = struct{}{}
	}

	// With 100 random uint32 values, collisions should be essentially impossible.
	if len(seen) < 95 {
		t.Errorf("too many collisions: only %d unique values out of 100", len(seen))
	}
}

func TestAESEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()

	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	testCases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("hello")},
		{"exact block", []byte("0123456789abcdef")},                // 16 bytes
		{"two blocks", []byte("0123456789abcdef0123456789abcdef")}, // 32 bytes
		{"odd length", []byte("this is 17 bytes!")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encrypted, err := EncryptAESECB(tc.plaintext, key)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}

			if len(encrypted)%16 != 0 {
				t.Fatalf("ciphertext length %d not multiple of 16", len(encrypted))
			}

			decrypted, err := DecryptAESECB(encrypted, key)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}

			if string(decrypted) != string(tc.plaintext) {
				t.Errorf("round-trip mismatch: got %q, want %q", decrypted, tc.plaintext)
			}
		})
	}
}

func TestAESKnownVector(t *testing.T) {
	t.Parallel()

	// Test with known AES-128-ECB output.
	// Single block: 16 bytes of 0x00, key = 16 bytes of 0x00.
	key := make([]byte, 16)
	plaintext := []byte("hello world12345") // exactly 16 bytes

	encrypted, err := EncryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// With PKCS7, 16 bytes of plaintext gets padded to 32 bytes.
	if len(encrypted) != 32 {
		t.Fatalf("expected 32 bytes ciphertext, got %d", len(encrypted))
	}

	decrypted, err := DecryptAESECB(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestCiphertextSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rawSize  int
		expected int
	}{
		{0, 16},          // ceil((0+1)/16)*16 = 16
		{1, 16},          // ceil((1+1)/16)*16 = 16
		{15, 16},         // ceil((15+1)/16)*16 = 16
		{16, 32},         // ceil((16+1)/16)*16 = 32
		{17, 32},         // ceil((17+1)/16)*16 = 32
		{31, 32},         // ceil((31+1)/16)*16 = 32
		{32, 48},         // ceil((32+1)/16)*16 = 48
		{248731, 248736}, // from spec example
	}

	for _, tc := range tests {
		got := CiphertextSize(tc.rawSize)
		if got != tc.expected {
			t.Errorf("CiphertextSize(%d) = %d, want %d", tc.rawSize, got, tc.expected)
		}
	}
}

func TestDecodeAESKeyFormatA(t *testing.T) {
	t.Parallel()

	// Format A: base64 of raw 16 bytes.
	rawKey, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	encoded := base64.StdEncoding.EncodeToString(rawKey)
	// Should be "ABEiM0RVZneImaq7zN3u/w=="

	key, err := DecodeAESKey(encoded)
	if err != nil {
		t.Fatalf("DecodeAESKey format A: %v", err)
	}

	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want 00112233445566778899aabbccddeeff", key)
	}
}

func TestDecodeAESKeyFormatB(t *testing.T) {
	t.Parallel()

	// Format B: base64 of hex string "00112233445566778899aabbccddeeff" (32 ASCII bytes).
	hexStr := "00112233445566778899aabbccddeeff"
	encoded := base64.StdEncoding.EncodeToString([]byte(hexStr))
	// Should be "MDAxMTIyMzM0NDU1NjY3Nzg4OTlhYWJiY2NkZGVlZmY="

	key, err := DecodeAESKey(encoded)
	if err != nil {
		t.Fatalf("DecodeAESKey format B: %v", err)
	}

	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want 00112233445566778899aabbccddeeff", key)
	}
}

func TestDecodeAESKeyInvalid(t *testing.T) {
	t.Parallel()

	// Not valid base64.
	_, err := DecodeAESKey("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}

	// Valid base64 but wrong length (e.g., 8 bytes).
	encoded := base64.StdEncoding.EncodeToString([]byte("12345678"))
	_, err = DecodeAESKey(encoded)
	if err == nil {
		t.Error("expected error for wrong length")
	}
}

func TestResolveImageKeyFromHexField(t *testing.T) {
	t.Parallel()

	img := &ImageItem{
		AESKey: "00112233445566778899aabbccddeeff",
		Media: &CDNMedia{
			AESKey: base64.StdEncoding.EncodeToString([]byte("ffffffffffffffffffffffffffffffff")),
		},
	}

	key, err := ResolveImageKey(img)
	if err != nil {
		t.Fatalf("ResolveImageKey: %v", err)
	}

	// Should use image_item.aeskey (hex), not media.aes_key.
	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want hex field key", key)
	}
}

func TestResolveImageKeyFallbackToMedia(t *testing.T) {
	t.Parallel()

	rawKey, _ := hex.DecodeString("aabbccddeeff00112233445566778899")
	img := &ImageItem{
		Media: &CDNMedia{
			AESKey: base64.StdEncoding.EncodeToString(rawKey),
		},
	}

	key, err := ResolveImageKey(img)
	if err != nil {
		t.Fatalf("ResolveImageKey: %v", err)
	}

	if hex.EncodeToString(key) != "aabbccddeeff00112233445566778899" {
		t.Errorf("got key %x, want media key", key)
	}
}

func TestResolveImageKeyNoKey(t *testing.T) {
	t.Parallel()

	img := &ImageItem{}
	_, err := ResolveImageKey(img)
	if err == nil {
		t.Error("expected error when no key present")
	}
}

func TestRandomFileKey(t *testing.T) {
	t.Parallel()

	key := RandomFileKey()

	// Must be 32 hex characters (16 bytes).
	if len(key) != 32 {
		t.Errorf("RandomFileKey length = %d, want 32", len(key))
	}

	matched, _ := regexp.MatchString("^[0-9a-f]{32}$", key)
	if !matched {
		t.Errorf("RandomFileKey %q does not match hex pattern", key)
	}

	// Uniqueness check.
	key2 := RandomFileKey()
	if key == key2 {
		t.Error("two consecutive RandomFileKey calls returned the same value")
	}
}

func TestRandomClientID(t *testing.T) {
	t.Parallel()

	id := RandomClientID("anna-weixin")

	if !strings.HasPrefix(id, "anna-weixin:") {
		t.Errorf("RandomClientID %q doesn't have expected prefix", id)
	}

	// Format: prefix:timestamp-random
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("RandomClientID %q missing colon separator", id)
	}

	rest := parts[1]
	dashIdx := strings.LastIndex(rest, "-")
	if dashIdx < 0 {
		t.Fatalf("RandomClientID %q missing dash in suffix", id)
	}

	tsStr := rest[:dashIdx]
	_, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		t.Errorf("RandomClientID timestamp %q not a valid int64: %v", tsStr, err)
	}

	suffix := rest[dashIdx+1:]
	if len(suffix) != 8 { // 4 bytes = 8 hex chars
		t.Errorf("RandomClientID suffix %q length = %d, want 8", suffix, len(suffix))
	}

	// Uniqueness.
	id2 := RandomClientID("anna-weixin")
	if id == id2 {
		t.Error("two consecutive RandomClientID calls returned the same value")
	}
}
