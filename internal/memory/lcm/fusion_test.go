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

// TestMinMax covers the normalization edges: a min maps to 0, a max to 1, and a
// no-spread lane (hi==lo) maps to 1 so a lone hit is never zeroed.
func TestMinMax(t *testing.T) {
	if v := minMax(5, 1, 5); v != 1 {
		t.Errorf("max should map to 1, got %v", v)
	}
	if v := minMax(1, 1, 5); v != 0 {
		t.Errorf("min should map to 0, got %v", v)
	}
	if v := minMax(3, 1, 5); v != 0.5 {
		t.Errorf("midpoint should map to 0.5, got %v", v)
	}
	if v := minMax(3, 3, 3); v != 1 {
		t.Errorf("no-spread lane should map to 1, got %v", v)
	}
}

// TestWeightedFuse_ZeroCosineIsPresentNotAbsent locks in the fix for the
// score-as-presence-sentinel bug: an item with a legitimate 0 cosine similarity
// (orthogonal to the query) is a present-but-worst semantic hit, and when it also
// tops the lexical lane its fused score must reflect the full lexical weight
// (0.5), not be mistaken for "absent from semantic" — which would coincidentally
// give the same number here, so the discriminating case is the flat semantic lane
// below.
func TestWeightedFuse_ZeroCosineIsPresentNotAbsent(t *testing.T) {
	now := time.Now().UTC()
	// Semantic lane is flat at 0 (everything orthogonal). With presence tracking,
	// each present semantic member normalizes to 1 (no spread), so "a" — also the
	// lexical max — scores 0.5*1 + 0.5*1 = 1.0. The old score==0 sentinel would
	// have zeroed the semantic contribution to 0.5.
	lexical := []memory.SearchResult{
		res("message", "a", 10.0, now),
		res("message", "b", 2.0, now),
	}
	semantic := []memory.SearchResult{
		res("message", "a", 0.0, now),
		res("message", "z", 0.0, now),
	}
	got := weightedFuse(lexical, semantic, 10)
	var aScore float64
	found := false
	for _, r := range got {
		if r.SourceID == "a" {
			aScore, found = r.Score, true
		}
	}
	if !found {
		t.Fatal("a missing from fused results")
	}
	if d := aScore - 1.0; d > 1e-9 || d < -1e-9 {
		t.Errorf("a should score 1.0 (lex-max + present-in-flat-semantic), got %v", aScore)
	}
}
