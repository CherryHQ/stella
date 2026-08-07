package library

import (
	"errors"
	"strings"
	"testing"
)

func TestLibraryDerivationKeyIsDeterministic(t *testing.T) {
	t.Parallel()
	raw := make([]byte, 32)
	for index := range raw {
		raw[index] = byte(index)
	}
	first, err := libraryDerivationKey(raw, MediaTypeText, TextParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := libraryDerivationKey(append([]byte(nil), raw...), MediaTypeText, TextParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("derivation keys = %q and %q", first, second)
	}
	raw[0]++
	changed, err := libraryDerivationKey(raw, MediaTypeText, TextParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("derivation key did not change with the raw snapshot")
	}
	changedType, err := libraryDerivationKey(append([]byte(nil), raw...), MediaTypeMarkdown, TextParserProfile)
	if err != nil {
		t.Fatal(err)
	}
	if changedType == changed {
		t.Fatal("derivation key did not change with the media type")
	}
	changedProfile, err := libraryDerivationKey(append([]byte(nil), raw...), MediaTypeMarkdown, "builtin-text:v2")
	if err != nil {
		t.Fatal(err)
	}
	if changedProfile == changedType {
		t.Fatal("derivation key did not change with the parser profile")
	}
	if _, err := libraryDerivationKey([]byte("short"), MediaTypeText, TextParserProfile); err == nil {
		t.Fatal("short raw hash was accepted")
	}
	if _, err := libraryDerivationKey(raw, "", TextParserProfile); err == nil {
		t.Fatal("empty media type was accepted")
	}
	if _, err := libraryDerivationKey(raw, MediaTypeText, ""); err == nil {
		t.Fatal("empty parser profile was accepted")
	}
}

func TestChunkBatchesRespectBothLimits(t *testing.T) {
	t.Parallel()
	chunks := make([]stagedChunk, MaxStagedChunksPerTx+1)
	for index := range chunks {
		chunks[index] = stagedChunk{Ordinal: int64(index), Content: "x"}
	}
	batches := chunkBatches(chunks)
	if len(batches) != 2 || len(batches[0]) != MaxStagedChunksPerTx || len(batches[1]) != 1 {
		t.Fatalf("count-bounded batches = %v", batchLengths(batches))
	}

	chunks = []stagedChunk{
		{Ordinal: 0, Content: strings.Repeat("a", MaxStagedContentBytesPerTx-1)},
		{Ordinal: 1, Content: "bb"},
	}
	batches = chunkBatches(chunks)
	if len(batches) != 2 || len(batches[0]) != 1 || len(batches[1]) != 1 {
		t.Fatalf("byte-bounded batches = %v", batchLengths(batches))
	}
}

func TestNormalizeParsedChunksRejectsOneOversizedChunk(t *testing.T) {
	t.Parallel()
	_, _, err := normalizeParsedChunks([]ParsedChunk{{
		Content: strings.Repeat("x", MaxStagedContentBytesPerTx+1),
	}})
	if !errors.Is(err, ErrParseResultLimit) {
		t.Fatalf("normalizeParsedChunks error = %v, want ErrParseResultLimit", err)
	}
}

func TestNormalizeParsedChunksRejectsInvalidLocator(t *testing.T) {
	t.Parallel()
	_, _, err := normalizeParsedChunks([]ParsedChunk{{
		Content: "valid text", Locator: ChunkLocator{ByteStart: 2, ByteEnd: 1},
	}})
	if !errors.Is(err, ErrInvalidParserData) {
		t.Fatalf("normalizeParsedChunks error = %v, want ErrInvalidParserData", err)
	}
}

func batchLengths(batches [][]stagedChunk) []int {
	lengths := make([]int, 0, len(batches))
	for _, batch := range batches {
		lengths = append(lengths, len(batch))
	}
	return lengths
}
