package search

import (
	"errors"
	"testing"
)

func TestMergeRRFBoostsDuplicateHits(t *testing.T) {
	lexical := []RankedHit[string]{
		{Key: "a", Value: "lexical-a"},
		{Key: "b", Value: "lexical-b"},
	}
	semantic := []RankedHit[string]{
		{Key: "b", Value: "semantic-b"},
		{Key: "c", Value: "semantic-c"},
	}

	got := MergeRRF(lexical, semantic, 10)
	if len(got) != 3 {
		t.Fatalf("MergeRRF returned %d hits, want 3: %+v", len(got), got)
	}
	if got[0].Key != "b" {
		t.Fatalf("top hit = %q, want duplicate b boosted first: %+v", got[0].Key, got)
	}
	if got[0].Value != "lexical-b" {
		t.Fatalf("duplicate value = %q, want first-seen lexical value", got[0].Value)
	}
	wantSources := []HitSource{HitSourceLexical, HitSourceSemantic}
	if !sameSources(got[0].Sources, wantSources) {
		t.Fatalf("duplicate sources = %+v, want %+v", got[0].Sources, wantSources)
	}
}

func TestMergeRRFLimitAndTieBreak(t *testing.T) {
	got := MergeRRF([]RankedHit[string]{
		{Key: "b", Value: "b"},
		{Key: "a", Value: "a"},
	}, nil, 1)
	if len(got) != 1 {
		t.Fatalf("MergeRRF limit returned %d hits, want 1", len(got))
	}
	if got[0].Key != "b" {
		t.Fatalf("limited top hit = %q, want lexical rank winner b", got[0].Key)
	}

	got = MergeRRF([]RankedHit[string]{{Key: "b", Value: "b"}}, []RankedHit[string]{{Key: "a", Value: "a"}}, 0)
	if len(got) != 2 {
		t.Fatalf("MergeRRF returned %d hits, want 2", len(got))
	}
	if got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("tie break order = %+v, want key order a,b", got)
	}
}

func TestMergeHybridSemanticUnavailableReturnsLexicalAndError(t *testing.T) {
	got, err := MergeHybrid([]RankedHit[string]{{Key: "a", Value: "a"}}, nil, ErrSemanticUnavailable, 10)
	if !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("MergeHybrid error = %v, want ErrSemanticUnavailable", err)
	}
	if len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("MergeHybrid unavailable result = %+v, want lexical fallback", got)
	}
	if !sameSources(got[0].Sources, []HitSource{HitSourceLexical}) {
		t.Fatalf("fallback sources = %+v", got[0].Sources)
	}
}

func TestMergeHybridPropagatesSemanticError(t *testing.T) {
	boom := errors.New("boom")
	got, err := MergeHybrid([]RankedHit[string]{{Key: "a", Value: "a"}}, nil, boom, 10)
	if !errors.Is(err, boom) {
		t.Fatalf("MergeHybrid error = %v, want boom", err)
	}
	if got != nil {
		t.Fatalf("MergeHybrid result = %+v, want nil on hard semantic error", got)
	}
}

func sameSources(got, want []HitSource) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
