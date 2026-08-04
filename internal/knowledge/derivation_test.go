package knowledge

import (
	"errors"
	"strings"
	"testing"
)

func TestKnowledgeDerivationKeyIsDeterministic(t *testing.T) {
	t.Parallel()
	raw := make([]byte, 32)
	for index := range raw {
		raw[index] = byte(index)
	}
	first, err := knowledgeDerivationKey(raw, MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	second, err := knowledgeDerivationKey(append([]byte(nil), raw...), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("derivation keys = %q and %q", first, second)
	}
	raw[0]++
	changed, err := knowledgeDerivationKey(raw, MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("derivation key did not change with the raw snapshot")
	}
	changedType, err := knowledgeDerivationKey(append([]byte(nil), raw...), MediaTypeDOCX)
	if err != nil {
		t.Fatal(err)
	}
	if changedType == changed {
		t.Fatal("derivation key did not change with the media type")
	}
	if _, err := knowledgeDerivationKey([]byte("short"), MediaTypePDF); err == nil {
		t.Fatal("short raw hash was accepted")
	}
	if _, err := knowledgeDerivationKey(raw, ""); err == nil {
		t.Fatal("empty media type was accepted")
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

func batchLengths(batches [][]stagedChunk) []int {
	lengths := make([]int, 0, len(batches))
	for _, batch := range batches {
		lengths = append(lengths, len(batch))
	}
	return lengths
}
