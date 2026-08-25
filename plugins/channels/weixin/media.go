package weixin

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/httpclient"
)

// EncryptAESECB encrypts plaintext using AES-128-ECB with PKCS7 padding.
func EncryptAESECB(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("weixin: aes cipher: %w", err)
	}

	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))

	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(ciphertext[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}

	return ciphertext, nil
}

// DecryptAESECB decrypts ciphertext using AES-128-ECB and removes PKCS7 padding.
func DecryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("weixin: aes cipher: %w", err)
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("weixin: ciphertext not multiple of block size")
	}

	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	return pkcs7Unpad(plaintext, aes.BlockSize)
}

// CiphertextSize returns the AES-128-ECB + PKCS7 ciphertext size for a given raw size.
// Formula: ceil((rawSize+1)/16) * 16
func CiphertextSize(rawSize int) int {
	return ((rawSize + 1 + aes.BlockSize - 1) / aes.BlockSize) * aes.BlockSize
}

// DecodeAESKey decodes an AES key from the base64 value in CDNMedia.aes_key.
// It handles two formats:
//   - Format A: base64(raw 16 bytes) → decode to 16 bytes, use directly
//   - Format B: base64(hex string) → decode to 32 hex chars, hex-decode to 16 bytes
func DecodeAESKey(aesKeyB64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyB64)
	if err != nil {
		return nil, fmt.Errorf("weixin: base64 decode aes_key: %w", err)
	}

	switch len(decoded) {
	case 16:
		// Format A: raw 16 bytes
		return decoded, nil
	case 32:
		// Format B: 32 hex characters → decode to 16 bytes
		if !isHexString(decoded) {
			return nil, fmt.Errorf("weixin: aes_key 32 bytes but not valid hex")
		}
		key, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("weixin: hex decode aes_key: %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("weixin: unexpected aes_key length %d after base64 decode", len(decoded))
	}
}

// ResolveImageKey determines the AES key for an image item.
// Precedence: image_item.aeskey (hex) > media.aes_key (base64).
func ResolveImageKey(imageItem *ImageItem) ([]byte, error) {
	if imageItem == nil {
		return nil, fmt.Errorf("weixin: nil image item")
	}

	// Priority 1: image_item.aeskey is a 32-char hex string.
	if imageItem.AESKey != "" {
		key, err := hex.DecodeString(imageItem.AESKey)
		if err != nil {
			return nil, fmt.Errorf("weixin: hex decode image aeskey: %w", err)
		}
		if len(key) != 16 {
			return nil, fmt.Errorf("weixin: image aeskey decoded to %d bytes, want 16", len(key))
		}
		return key, nil
	}

	// Priority 2: media.aes_key is base64.
	if imageItem.Media != nil && imageItem.Media.AESKey != "" {
		return DecodeAESKey(imageItem.Media.AESKey)
	}

	return nil, fmt.Errorf("weixin: no AES key found for image")
}

// UploadToCDN uploads encrypted data to the WeChat CDN.
// uploadFullURL, when non-empty, is used directly as the PUT target (priority over uploadParam).
// It performs one request because any transport/server error may follow an
// accepted upload and therefore has an unknown outcome.
// Returns the x-encrypted-param response header value.
func UploadToCDN(cdnBaseURL, uploadFullURL, uploadParam, filekey string, encrypted []byte) (string, error) {
	if cdnBaseURL == "" {
		cdnBaseURL = DefaultCDNBaseURL
	}

	client := httpclient.NewWithTimeout(60 * time.Second)

	r := client.R().
		SetHeader("Content-Type", "application/octet-stream").
		SetBody(encrypted)

	var url string
	if uploadFullURL != "" {
		url = uploadFullURL
	} else {
		url = cdnBaseURL + "/c2c/upload"
		r = r.SetQueryParam("encrypted_query_param", uploadParam).
			SetQueryParam("filekey", filekey)
	}

	resp, err := r.Post(url)
	if err != nil {
		return "", fmt.Errorf("weixin: cdn upload outcome unknown: %w", err)
	}

	if resp.StatusCode() >= 400 && resp.StatusCode() < 500 {
		errMsg := resp.Header().Get("x-error-message")
		return "", fmt.Errorf("weixin: cdn upload client error %d: %s", resp.StatusCode(), errMsg)
	}

	if resp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("weixin: cdn upload outcome unknown: status %d", resp.StatusCode())
	}

	encryptedParam := resp.Header().Get("x-encrypted-param")
	if encryptedParam == "" {
		return "", fmt.Errorf("weixin: cdn upload missing x-encrypted-param header")
	}
	return encryptedParam, nil
}

// DownloadFromCDN downloads data from the WeChat CDN.
// fullURL, when non-empty, is used directly (priority over constructing from encryptedQueryParam).
func DownloadFromCDN(cdnBaseURL, fullURL, encryptedQueryParam string) ([]byte, error) {
	if cdnBaseURL == "" {
		cdnBaseURL = DefaultCDNBaseURL
	}

	client := httpclient.NewWithTimeout(60 * time.Second)

	r := client.R().SetDoNotParseResponse(true)
	var url string
	if fullURL != "" {
		url = fullURL
	} else {
		url = cdnBaseURL + "/c2c/download"
		r = r.SetQueryParam("encrypted_query_param", encryptedQueryParam)
	}

	resp, err := r.Get(url)
	if err != nil {
		return nil, fmt.Errorf("weixin: cdn download: %w", err)
	}
	body := resp.RawBody()
	defer func() { _ = body.Close() }()

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("weixin: cdn download status %d", resp.StatusCode())
	}

	data, err := io.ReadAll(io.LimitReader(body, channel.MaxInboundAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("weixin: read cdn download: %w", err)
	}
	if len(data) > channel.MaxInboundAttachmentBytes {
		return nil, fmt.Errorf("weixin: cdn download exceeds %d bytes", channel.MaxInboundAttachmentBytes)
	}
	return data, nil
}

// RandomFileKey generates a random 16-byte hex string for CDN upload filekey.
func RandomFileKey() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// RandomClientID generates a unique client_id with the given prefix.
// Format: prefix:timestamp-random
func RandomClientID(prefix string) string {
	ts := time.Now().UnixMilli()
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	suffix := hex.EncodeToString(buf[:])
	return prefix + ":" + strconv.FormatInt(ts, 10) + "-" + suffix
}

// silkToWav transcodes a SILK-encoded audio buffer to a WAV container.
// Returns nil if transcoding is unavailable or fails; callers should fall back
// to raw SILK or transcription text.
//
// SILK is a Tencent proprietary format. A native Go decoder is not yet available
// without CGO; this is a stub for a future implementation.
func silkToWav(_ []byte) []byte {
	return nil
}

// --- internal helpers ---

// pkcs7Pad pads data to a multiple of blockSize using PKCS7.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	pad := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, pad...)
}

// pkcs7Unpad removes PKCS7 padding.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("weixin: invalid padded data length")
	}

	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize {
		return nil, fmt.Errorf("weixin: invalid PKCS7 padding value %d", padding)
	}

	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) {
			return nil, fmt.Errorf("weixin: invalid PKCS7 padding")
		}
	}

	return data[:len(data)-padding], nil
}

// isHexString returns true if all bytes are valid hex characters [0-9a-fA-F].
func isHexString(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
