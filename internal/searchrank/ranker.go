package searchrank

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Field is one searchable text field in a document. Weight lets callers make a
// domain field such as a skill description more important than auxiliary fields.
type Field struct {
	Name   string
	Text   string
	Weight float64
}

// Document is the minimal searchable unit consumed by the deterministic ranker.
type Document struct {
	ID     string
	Fields []Field
}

// Result is a compact search hit with enough evidence for tool responses.
type Result struct {
	ID            string   `json:"id"`
	Score         float64  `json:"score"`
	MatchedFields []string `json:"matched_fields"`
	Snippet       string   `json:"snippet"`
}

// Rank runs a small deterministic BM25-style ranker over in-memory documents.
// It intentionally has no embeddings, recency, or usage signals; callers add
// domain-specific boosts after this step when they need them.
func Rank(query string, docs []Document, limit int) []Result {
	queryTerms := uniqueTokens(tokenize(query))
	if len(queryTerms) == 0 || len(docs) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(docs) {
		limit = len(docs)
	}

	prepared := make([]preparedDoc, 0, len(docs))
	docFreq := make(map[string]int, len(queryTerms))
	totalLen := 0
	for _, doc := range docs {
		p := prepareDoc(doc)
		prepared = append(prepared, p)
		totalLen += p.length
		seen := map[string]struct{}{}
		for _, term := range queryTerms {
			if p.totalTermFreq(term) > 0 {
				seen[term] = struct{}{}
			}
		}
		for term := range seen {
			docFreq[term]++
		}
	}
	avgLen := float64(totalLen) / float64(len(prepared))
	if avgLen <= 0 {
		avgLen = 1
	}

	results := make([]Result, 0, len(prepared))
	for _, doc := range prepared {
		score, matched := scoreDoc(doc, queryTerms, docFreq, len(prepared), avgLen)
		if score <= 0 {
			continue
		}
		results = append(results, Result{
			ID:            doc.id,
			Score:         score,
			MatchedFields: matched,
			Snippet:       makeSnippet(doc.fields, queryTerms),
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	return results[:min(limit, len(results))]
}

type preparedDoc struct {
	id     string
	fields []preparedField
	length int
}

type preparedField struct {
	name   string
	text   string
	weight float64
	tf     map[string]int
}

func prepareDoc(doc Document) preparedDoc {
	p := preparedDoc{id: doc.ID, fields: make([]preparedField, 0, len(doc.Fields))}
	for _, field := range doc.Fields {
		weight := field.Weight
		if weight <= 0 {
			weight = 1
		}
		tokens := tokenize(field.Text)
		p.length += len(tokens)
		p.fields = append(p.fields, preparedField{
			name:   field.Name,
			text:   field.Text,
			weight: weight,
			tf:     termFrequency(tokens),
		})
	}
	return p
}

func (d preparedDoc) totalTermFreq(term string) int {
	total := 0
	for _, field := range d.fields {
		total += field.tf[term]
	}
	return total
}

func scoreDoc(doc preparedDoc, queryTerms []string, docFreq map[string]int, docCount int, avgLen float64) (float64, []string) {
	const (
		k1 = 1.2
		b  = 0.75
	)

	length := float64(doc.length)
	if length <= 0 {
		length = 1
	}
	seenFields := map[string]struct{}{}
	var score float64
	for _, term := range queryTerms {
		df := docFreq[term]
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(docCount)-float64(df)+0.5)/(float64(df)+0.5))
		for _, field := range doc.fields {
			tf := field.tf[term]
			if tf == 0 {
				continue
			}
			freq := float64(tf)
			denom := freq + k1*(1-b+b*(length/avgLen))
			score += field.weight * idf * (freq * (k1 + 1) / denom)
			seenFields[field.name] = struct{}{}
		}
	}
	if score <= 0 {
		return 0, nil
	}
	fields := make([]string, 0, len(seenFields))
	for name := range seenFields {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return score, fields
}

func termFrequency(tokens []string) map[string]int {
	tf := make(map[string]int, len(tokens))
	for _, token := range tokens {
		tf[token]++
	}
	return tf
}

func uniqueTokens(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, strings.ToLower(b.String()))
		b.Reset()
	}
	for _, r := range s {
		if isCJK(r) {
			flush()
			out = append(out, strings.ToLower(string(r)))
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return out
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF)
}

func makeSnippet(fields []preparedField, queryTerms []string) string {
	for _, term := range queryTerms {
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field.text), term) {
				return trimSnippet(field.text)
			}
		}
	}
	for _, field := range fields {
		if strings.TrimSpace(field.text) != "" {
			return trimSnippet(field.text)
		}
	}
	return ""
}

func trimSnippet(s string) string {
	s = strings.TrimSpace(s)
	const maxRunes = 160
	if len([]rune(s)) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}
