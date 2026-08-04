package knowledge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	// XbergProcessorKey contains every parser setting that can change persisted
	// chunks. A profile change therefore creates a different derivation instead
	// of mutating an existing generation.
	XbergProcessorKey = "xberg:1.0.4;content=markdown;chunker=markdown;max_chars=1000;max_overlap=200;prepend_heading_context=false;ocr=false;cache=false;extract_pages=true;page_markers=false;extract_images=false;pdf_extract_images=false;pdf_ocr_inline_images=false"

	MaxStagedChunksPerTx       = 500
	MaxStagedContentBytesPerTx = 2 << 20
)

type stagedChunk struct {
	Ordinal       int64
	Content       string
	LocatorJSON   string
	ContentSHA256 [sha256.Size]byte
}

// knowledgeDerivationKey binds every parser input that can change persisted
// chunks: immutable raw bytes, trusted media type, and exact parser profile.
// The key is opaque; callers compare it but never parse it.
func knowledgeDerivationKey(rawSHA256 []byte, mediaType string) (string, error) {
	if len(rawSHA256) != sha256.Size {
		return "", fmt.Errorf("invalid knowledge raw SHA-256 length %d", len(rawSHA256))
	}
	if mediaType == "" {
		return "", fmt.Errorf("knowledge media type is required for derivation")
	}
	hash := sha256.New()
	_, _ = hash.Write(rawSHA256)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(mediaType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(XbergProcessorKey))
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// normalizeParsedChunks creates the exact rows staged in PostgreSQL and the
// expected generation digest. It performs a second resource-boundary check so
// a custom Parser cannot bypass the Xberg adapter's limits.
func normalizeParsedChunks(chunks []ParsedChunk) ([]stagedChunk, []byte, error) {
	if len(chunks) == 0 {
		return nil, nil, ErrNoExtractedText
	}
	if len(chunks) > DefaultMaxChunks {
		return nil, nil, fmt.Errorf("%w: too many chunks", ErrParseResultLimit)
	}

	staged := make([]stagedChunk, 0, len(chunks))
	totalBytes := 0
	for ordinal, chunk := range chunks {
		if !hasEffectiveText(chunk.Content) {
			return nil, nil, fmt.Errorf("%w: chunk %d has no effective text", ErrInvalidXbergJSON, ordinal)
		}
		contentBytes := len(chunk.Content)
		if contentBytes > MaxStagedContentBytesPerTx {
			return nil, nil, fmt.Errorf("%w: one chunk exceeds the staging transaction limit", ErrParseResultLimit)
		}
		totalBytes += contentBytes
		if totalBytes > DefaultMaxChunkBytes {
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
		})
	}
	return staged, stagedContentDigest(staged), nil
}

// stagedContentDigest matches GetKnowledgeChunkSetIntegrity: each row adds an
// eight-byte big-endian ordinal followed by its 32-byte content hash.
func stagedContentDigest(chunks []stagedChunk) []byte {
	hash := sha256.New()
	var ordinal [8]byte
	for _, chunk := range chunks {
		binary.BigEndian.PutUint64(ordinal[:], uint64(chunk.Ordinal))
		_, _ = hash.Write(ordinal[:])
		_, _ = hash.Write(chunk.ContentSHA256[:])
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
