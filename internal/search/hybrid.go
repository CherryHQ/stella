package search

import (
	"errors"
	"slices"
	"sort"
)

const rrfK = 60

// ErrSemanticUnavailable marks an intentional degraded path: callers may still
// receive lexical results alongside this error when semantic search is disabled
// or not yet configured. Do not treat it like a hard failure without checking
// the returned hits.
var ErrSemanticUnavailable = errors.New("semantic search unavailable")

type HitSource string

const (
	HitSourceLexical  HitSource = "lexical"
	HitSourceSemantic HitSource = "semantic"
)

type RankedHit[T any] struct {
	Key   string
	Value T
}

type MergedHit[T any] struct {
	Key     string
	Value   T
	Score   float64
	Sources []HitSource
}

func MergeRRF[T any](lexical, semantic []RankedHit[T], limit int) []MergedHit[T] {
	merged := map[string]*MergedHit[T]{}
	order := make([]string, 0, len(lexical)+len(semantic))
	add := func(source HitSource, hits []RankedHit[T]) {
		for i, hit := range hits {
			if hit.Key == "" {
				continue
			}
			item, ok := merged[hit.Key]
			if !ok {
				item = &MergedHit[T]{Key: hit.Key, Value: hit.Value}
				merged[hit.Key] = item
				order = append(order, hit.Key)
			}
			item.Score += rrfScore(i + 1)
			item.Sources = appendSource(item.Sources, source)
		}
	}
	add(HitSourceLexical, lexical)
	add(HitSourceSemantic, semantic)

	items := make([]MergedHit[T], 0, len(order))
	for _, key := range order {
		items = append(items, *merged[key])
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Key < items[j].Key
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// MergeHybrid combines lexical and semantic ranked hits. If semanticErr wraps
// ErrSemanticUnavailable, it returns the lexical-only RRF results together with
// ErrSemanticUnavailable so callers can surface an explicit degraded state while
// still using the valid lexical results. Other semantic errors are hard failures
// and return no hits.
func MergeHybrid[T any](lexical, semantic []RankedHit[T], semanticErr error, limit int) ([]MergedHit[T], error) {
	if semanticErr != nil {
		if errors.Is(semanticErr, ErrSemanticUnavailable) {
			return MergeRRF(lexical, nil, limit), ErrSemanticUnavailable
		}
		return nil, semanticErr
	}
	return MergeRRF(lexical, semantic, limit), nil
}

func rrfScore(rank int) float64 {
	return 1.0 / float64(rrfK+rank)
}

func appendSource(sources []HitSource, source HitSource) []HitSource {
	if slices.Contains(sources, source) {
		return sources
	}
	return append(sources, source)
}
