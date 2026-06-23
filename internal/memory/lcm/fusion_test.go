package lcm

import (
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

func res(srcType, id string, score float64, at time.Time) memory.SearchResult {
	return memory.SearchResult{SourceType: srcType, SourceID: id, Score: score, OccurredAt: at}
}

// TestWeightedFuse_MergesLanesByKey verifies that a result present in both lanes
// merges into one row whose fused score sums both lanes' normalized contributions
// (top-of-both beats single-lane hits), and that the lanes' incomparable raw
// scales are normalized away before weighting.
func TestWeightedFuse_MergesLanesByKey(t *testing.T) {
	now := time.Now().UTC()
	// Lexical scores on a large BM25-like scale; semantic on cosine [0,1]. "b" tops
	// both lanes, so after per-lane min-max it scores 0.5*1 + 0.5*1 = 1.0 and wins.
	lexical := []memory.SearchResult{
		res("message", "b", 12.0, now),
		res("message", "a", 4.0, now),
	}
	semantic := []memory.SearchResult{
		res("message", "b", 0.9, now),
		res("summary", "c", 0.5, now),
	}

	got := weightedFuse(lexical, semantic, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 merged rows (b deduped), got %d: %+v", len(got), got)
	}
	if got[0].SourceID != "b" {
		t.Fatalf("expected b first (tops both lanes), got %q", got[0].SourceID)
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("b should outscore the rest: %v vs %v", got[0].Score, got[1].Score)
	}
	// b is lex-max (norm 1) and sem-max (norm 1): 0.5 + 0.5.
	if d := got[0].Score - 1.0; d > 1e-9 || d < -1e-9 {
		t.Errorf("b fused score = %v, want 1.0", got[0].Score)
	}
}

// TestWeightedFuse_SingleLaneHitSurvives checks that a result appearing in only
// one lane still contributes that lane's normalized score (and zero from the
// other) rather than being dropped or zeroed out.
func TestWeightedFuse_SingleLaneHitSurvives(t *testing.T) {
	now := time.Now().UTC()
	// Only-semantic hit "c" must outrank an only-lexical hit when its lane-normalized
	// score is higher: both are lone members? No -- give each lane two members so
	// normalization has spread.
	lexical := []memory.SearchResult{
		res("message", "a", 10.0, now),
		res("message", "d", 1.0, now),
	}
	semantic := []memory.SearchResult{
		res("summary", "c", 0.9, now),
		res("summary", "e", 0.1, now),
	}
	got := weightedFuse(lexical, semantic, 10)
	if len(got) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(got))
	}
	// a (lex-max) and c (sem-max) both normalize to 1 in their lane, weighted to 0.5.
	// They tie at 0.5; the rest score lower. Assert both top the list.
	top := map[string]bool{got[0].SourceID: true, got[1].SourceID: true}
	if !top["a"] || !top["c"] {
		t.Errorf("expected lane-leaders a and c on top, got %q and %q", got[0].SourceID, got[1].SourceID)
	}
	for _, r := range got[2:] {
		if r.Score >= got[0].Score {
			t.Errorf("non-leader %q scored %v >= leader %v", r.SourceID, r.Score, got[0].Score)
		}
	}
}

// TestNormalizeLane covers the degenerate spreads: an absent member (score 0) maps
// to 0, and a lane with no spread (all equal) maps present members to 1 so a lone
// hit is never zeroed.
func TestNormalizeLane(t *testing.T) {
	lane := []memory.SearchResult{res("m", "x", 5, time.Time{}), res("m", "y", 1, time.Time{})}
	if v := normalizeLane(0, lane); v != 0 {
		t.Errorf("absent member should map to 0, got %v", v)
	}
	if v := normalizeLane(5, lane); v != 1 {
		t.Errorf("max should map to 1, got %v", v)
	}
	if v := normalizeLane(1, lane); v != 0 {
		t.Errorf("min should map to 0, got %v", v)
	}

	flat := []memory.SearchResult{res("m", "x", 3, time.Time{})}
	if v := normalizeLane(3, flat); v != 1 {
		t.Errorf("no-spread lone hit should map to 1, got %v", v)
	}
}
