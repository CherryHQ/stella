package library

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type stagedChunk struct {
	Ordinal       int64
	Content       string
	LocatorJSON   string
	ContentSHA256 [sha256.Size]byte
	LocatorSHA256 [sha256.Size]byte
}

// libraryDerivationKey binds every parser input that can change persisted
// chunks: immutable raw bytes, trusted media type, and exact parser profile.
// The key is opaque; callers compare it but never parse it.
func libraryDerivationKey(rawSHA256 []byte, mediaType, parserProfile string) (string, error) {
	if len(rawSHA256) != sha256.Size {
		return "", fmt.Errorf("invalid library raw SHA-256 length %d", len(rawSHA256))
	}
	if mediaType == "" {
		return "", fmt.Errorf("library media type is required for derivation")
	}
	if parserProfile == "" {
		return "", fmt.Errorf("library parser profile is required for derivation")
	}
	hash := sha256.New()
	_, _ = hash.Write(rawSHA256)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(mediaType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(parserProfile))
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// normalizeParsedChunks creates the exact rows staged in PostgreSQL and the
// expected generation digest. It performs a second resource-boundary check so
// a custom Parser cannot bypass the Library service's limits.
func normalizeParsedChunks(chunks []ParsedChunk) ([]stagedChunk, []byte, error) {
	if len(chunks) == 0 {
		return nil, nil, ErrNoExtractedText
	}
	if len(chunks) > MaxParsedChunks {
		return nil, nil, fmt.Errorf("%w: too many chunks", ErrParseResultLimit)
	}

	staged := make([]stagedChunk, 0, len(chunks))
	totalBytes := 0
	hasDocumentText := false
	for ordinal, chunk := range chunks {
		if strings.TrimSpace(chunk.Content) == "" {
			return nil, nil, fmt.Errorf("%w: chunk %d is empty or whitespace-only", ErrInvalidParserData, ordinal)
		}
		if hasEffectiveText(chunk.Content) {
			hasDocumentText = true
		}
		if chunk.Locator.ByteStart < 0 || chunk.Locator.ByteEnd < chunk.Locator.ByteStart {
			return nil, nil, fmt.Errorf("%w: chunk %d has invalid byte offsets", ErrInvalidParserData, ordinal)
		}
		contentBytes := len(chunk.Content)
		if contentBytes > MaxStagedContentBytesPerTx {
			return nil, nil, fmt.Errorf("%w: one chunk exceeds the staging transaction limit", ErrParseResultLimit)
		}
		totalBytes += contentBytes
		if totalBytes > MaxParsedChunkContentBytes {
			return nil, nil, fmt.Errorf("%w: chunk content is too large", ErrParseResultLimit)
		}
		locator, err := json.Marshal(chunk.Locator)
		if err != nil {
			return nil, nil, fmt.Errorf("encode chunk %d locator: %w", ordinal, err)
		}
		staged = append(staged, stagedChunk{
			Ordinal:       int64(ordinal),
			Content:       chunk.Content,
			LocatorJSON:   string(locator),
			ContentSHA256: sha256.Sum256([]byte(chunk.Content)),
			LocatorSHA256: sha256.Sum256(locator),
		})
	}
	if !hasDocumentText {
		return nil, nil, ErrNoExtractedText
	}
	return staged, stagedContentDigest(staged), nil
}

// stagedContentDigest matches GetLibraryChunkSetIntegrity: each row adds an
// eight-byte big-endian ordinal followed by the 32-byte content hash and the
// 32-byte hash of the exact marshaled locator bytes.
func stagedContentDigest(chunks []stagedChunk) []byte {
	hash := sha256.New()
	var ordinal [8]byte
	for _, chunk := range chunks {
		binary.BigEndian.PutUint64(ordinal[:], uint64(chunk.Ordinal))
		_, _ = hash.Write(ordinal[:])
		_, _ = hash.Write(chunk.ContentSHA256[:])
		_, _ = hash.Write(chunk.LocatorSHA256[:])
	}
	return hash.Sum(nil)
}

func chunkBatches(chunks []stagedChunk) [][]stagedChunk {
	batches := make([][]stagedChunk, 0, (len(chunks)+MaxStagedChunksPerTx-1)/MaxStagedChunksPerTx)
	for start := 0; start < len(chunks); {
		end := start
		contentBytes := 0
		for end < len(chunks) && end-start < MaxStagedChunksPerTx {
			nextBytes := len(chunks[end].Content)
			if end > start && contentBytes+nextBytes > MaxStagedContentBytesPerTx {
				break
			}
			contentBytes += nextBytes
			end++
		}
		batches = append(batches, chunks[start:end])
		start = end
	}
	return batches
}
