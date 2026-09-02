package lcm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/model/embedding"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Default retrieval limits.
const (
	defaultSearchLimit  = 20
	defaultExpandTokens = 4000
	maxContentSnippet   = 500
)

// QueryEmbedder turns a search query into a vector plus the vector space it
// belongs to, so the retrieval engine can run KNN against the matching index.
// It is optional: when nil, search is pure BM25 (embedding.Service satisfies it).
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) (pgvector.Vector, string, error)
}

// retrievalEngine provides search and exploration of compacted history.
type retrievalEngine struct {
	q        *sqlc.Queries
	embedder QueryEmbedder // nil => lexical-only (no semantic lane)
	log      *slog.Logger
}

func newRetrievalEngine(q *sqlc.Queries, log *slog.Logger) *retrievalEngine {
	if log == nil {
		log = slog.Default()
	}
	return &retrievalEngine{q: q, log: log}
}

// Search implements memory.Searcher.
func (p *Provider) Search(ctx context.Context, session memory.Session, query memory.SearchQuery) ([]memory.SearchResult, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return nil, err
	}
	return p.retrieval.search(ctx, session.UserID, session.AgentID, query)
}

func (r *retrievalEngine) search(ctx context.Context, userID, agentID string, query memory.SearchQuery) ([]memory.SearchResult, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	scope := scopeBoth
	switch query.Scope {
	case memory.SearchScopeMessages:
		scope = scopeMessages
	case memory.SearchScopeSummaries:
		scope = scopeSummaries
	}

	// normalizeQuery folds punctuation to spaces before pg_search tokenizes it, so
	// the jieba tokenizer never emits a punctuation token that matches unrelated
	// rows; a query with no letters/digits normalizes to "" and short-circuits.
	match := normalizeQuery(query.Text)
	if match == "" {
		return nil, nil
	}

	var msgResults, sumResults []memory.SearchResult

	if scope == scopeMessages || scope == scopeBoth {
		msgs, err := r.q.SearchMessages(ctx, sqlc.SearchMessagesParams{
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Match:   match,
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, msg := range msgs {
			msgResults = append(msgResults, memory.SearchResult{
				SourceType:        itemTypeMessage,
				SourceID:          fmt.Sprint(msg.ID),
				Content:           searchSnippet(msg.Snippet, msg.Content),
				Score:             msg.Score,
				OccurredAt:        msg.CreatedAt.UTC(),
				SessionID:         msg.SessionID,
				ConversationTitle: msg.ConversationTitle.String,
			})
		}
	}

	if scope == scopeSummaries || scope == scopeBoth {
		sums, err := r.q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Match:   match,
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search summaries: %w", err)
		}
		for _, s := range sums {
			sumResults = append(sumResults, memory.SearchResult{
				SourceType:        itemTypeSummary,
				SourceID:          s.ID,
				Content:           searchSnippet(s.Snippet, s.Content),
				Score:             s.Score,
				OccurredAt:        summaryContentTime(s.LatestAt, s.CreatedAt),
				SessionID:         s.SessionID,
				ConversationTitle: s.ConversationTitle.String,
			})
		}
	}

	// Lexical lane: a single-scope query keeps its one BM25-ranked list as-is
	// (within one index the raw score is a meaningful absolute relevance signal);
	// a both-scope query fuses the two indexes with RRF, whose per-index scores
	// are not directly comparable.
	var lexical []memory.SearchResult
	switch scope {
	case scopeMessages:
		lexical = capResults(msgResults, limit)
	case scopeSummaries:
		lexical = capResults(sumResults, limit)
	default:
		lexical = fuseRRF(msgResults, sumResults, limit)
	}

	// Without a configured embedder there is no semantic lane: return lexical
	// results unchanged (pure BM25, the historical behavior).
	if r.embedder == nil {
		return lexical, nil
	}

	// Semantic lane: embed the raw query and KNN the matching vector space. The
	// lane is best-effort — an embedder or KNN failure degrades to lexical-only
	// rather than failing the search, so a flaky embedding provider never takes
	// search down.
	vector, err := r.vectorSearch(ctx, userID, agentID, query.Text, scope, limit)
	if err != nil {
		r.log.Warn("lcm: semantic lane unavailable, using lexical only", "error", err)
		return lexical, nil
	}
	if len(vector) == 0 {
		return lexical, nil
	}
	return weightedFuse(lexical, vector, limit), nil
}

// vectorSearch runs the semantic lane: it embeds the query, then KNN-searches the
// in-scope embedding sidecars in the resulting vector space and merges the hits
// by cosine similarity (comparable across the two indexes since they share the
// space and metric).
func (r *retrievalEngine) vectorSearch(ctx context.Context, userID, agentID, text, scope string, limit int) ([]memory.SearchResult, error) {
	qvec, model, err := r.embedder.EmbedQuery(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	// An empty space key means the embedder reported no vector space (lane disabled
	// or unconfigured): there is no semantic lane to run, so return no hits and let
	// the caller use lexical results unchanged.
	if model == "" {
		return nil, nil
	}
	// QueryEmbedder is intentionally an interface, so validate here as well as in
	// embedding.Service. An invalid cosine direction disables only the semantic
	// lane; search() will keep the lexical results.
	if err := embedding.ValidateStorageVector(qvec.Slice()); err != nil {
		return nil, fmt.Errorf("invalid query vector: %w", err)
	}

	var results []memory.SearchResult
	if scope == scopeMessages || scope == scopeBoth {
		rows, err := r.q.SearchMessageEmbeddings(ctx, sqlc.SearchMessageEmbeddingsParams{
			Query:   qvec,
			Model:   model,
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search message embeddings: %w", err)
		}
		for _, m := range rows {
			results = append(results, memory.SearchResult{
				SourceType:        itemTypeMessage,
				SourceID:          fmt.Sprint(m.ID),
				Content:           searchSnippet("", m.Content),
				Score:             m.Score,
				OccurredAt:        m.CreatedAt.UTC(),
				SessionID:         m.SessionID,
				ConversationTitle: m.ConversationTitle.String,
			})
		}
	}
	if scope == scopeSummaries || scope == scopeBoth {
		rows, err := r.q.SearchSummaryEmbeddings(ctx, sqlc.SearchSummaryEmbeddingsParams{
			Query:   qvec,
			Model:   model,
			UserID:  pgtype.Text{String: userID, Valid: true},
			AgentID: pgnull.Text(agentID),
			Limit:   int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("search summary embeddings: %w", err)
		}
		for _, s := range rows {
			results = append(results, memory.SearchResult{
				SourceType:        itemTypeSummary,
				SourceID:          s.ID,
				Content:           searchSnippet("", s.Content),
				Score:             s.Score,
				OccurredAt:        summaryContentTime(s.LatestAt, s.CreatedAt),
				SessionID:         s.SessionID,
				ConversationTitle: s.ConversationTitle.String,
			})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].OccurredAt.After(results[j].OccurredAt)
	})
	return capResults(results, limit), nil
}

// rrfK is the Reciprocal Rank Fusion damping constant: 60, the value from the
// original RRF paper and the de facto default. Larger values flatten the
// contribution curve so rank position matters less.
const rrfK = 60

// fuseRRF merges two BM25-ranked result lists with Reciprocal Rank Fusion. The
// two lists come from independent pg_search indexes whose raw scores share no
// common scale (IDF is per-index), so comparing them directly mis-orders the
// merge. RRF instead fuses on 1/(k+rank): each input must already be sorted
// best-first, so a slice index is its within-list rank, and the fused value
// replaces the now-meaningless cross-index raw score.
func fuseRRF(a, b []memory.SearchResult, limit int) []memory.SearchResult {
	merged := make([]memory.SearchResult, 0, len(a)+len(b))
	for i := range a {
		a[i].Score = 1.0 / float64(rrfK+i+1)
		merged = append(merged, a[i])
	}
	for i := range b {
		b[i].Score = 1.0 / float64(rrfK+i+1)
		merged = append(merged, b[i])
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].OccurredAt.After(merged[j].OccurredAt)
	})
	return capResults(merged, limit)
}

// Lexical and semantic lane weights for hybrid fusion. They sum to 1 and split
// the score evenly: BM25 lexical precision and vector recall are treated as
// equally trustworthy signals. Exposed as constants so the balance is a single
// obvious knob rather than a magic literal in the loop.
const (
	weightLexical  = 0.5
	weightSemantic = 0.5
)

// weightedFuse blends the lexical (BM25/RRF) and semantic (cosine) lanes into one
// ranking. The two lanes score on incomparable scales — BM25 is unbounded and
// IDF-relative, cosine similarity is bounded [0,1] — so each lane is min-max
// normalized to [0,1] independently before weighting. A result present in only
// one lane contributes that lane's normalized score and zero from the other, so a
// strong single-lane hit still surfaces; a result in both lanes is reinforced.
// The fused score replaces the per-lane raw scores, which are no longer
// meaningful after normalization. Identity is SourceType+"/"+SourceID so the same
// underlying item from both lanes merges into one row.
//
// Lane membership is tracked explicitly (hasLex/hasSem) rather than inferred from
// a zero score: a cosine similarity of 0 (orthogonal vectors) is a legitimate
// present-but-worst hit, distinct from "absent from this lane", and the two must
// not collapse.
func weightedFuse(lexical, semantic []memory.SearchResult, limit int) []memory.SearchResult {
	type fused struct {
		res            memory.SearchResult
		lex, sem       float64
		hasLex, hasSem bool
	}
	order := make([]string, 0, len(lexical)+len(semantic))
	byKey := make(map[string]*fused, len(lexical)+len(semantic))
	get := func(r memory.SearchResult) *fused {
		key := r.SourceType + "/" + r.SourceID
		f, ok := byKey[key]
		if !ok {
			f = &fused{res: r}
			byKey[key] = f
			order = append(order, key)
		}
		return f
	}
	for _, r := range lexical {
		f := get(r)
		f.lex, f.hasLex = r.Score, true
	}
	for _, r := range semantic {
		f := get(r)
		f.sem, f.hasSem = r.Score, true
	}

	loLex, hiLex := laneBounds(lexical)
	loSem, hiSem := laneBounds(semantic)

	merged := make([]memory.SearchResult, 0, len(order))
	for _, key := range order {
		f := byKey[key]
		var normLex, normSem float64
		if f.hasLex {
			normLex = minMax(f.lex, loLex, hiLex)
		}
		if f.hasSem {
			normSem = minMax(f.sem, loSem, hiSem)
		}
		f.res.Score = weightLexical*normLex + weightSemantic*normSem
		merged = append(merged, f.res)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].OccurredAt.After(merged[j].OccurredAt)
	})
	return capResults(merged, limit)
}

// laneBounds returns the min and max score in a lane (0,0 for an empty lane, which
// has no members to normalize).
func laneBounds(lane []memory.SearchResult) (lo, hi float64) {
	if len(lane) == 0 {
		return 0, 0
	}
	lo, hi = lane[0].Score, lane[0].Score
	for _, r := range lane {
		if r.Score < lo {
			lo = r.Score
		}
		if r.Score > hi {
			hi = r.Score
		}
	}
	return lo, hi
}

// minMax scales score into [0,1] given its lane bounds. A lane with no spread
// (one member, or all-equal scores) maps every present member to 1 so a lone hit
// is not zeroed out. Callers must only pass scores of members actually present in
// the lane.
func minMax(score, lo, hi float64) float64 {
	if hi == lo {
		return 1
	}
	return (score - lo) / (hi - lo)
}

// capResults truncates to the top limit, leaving order untouched.
func capResults(results []memory.SearchResult, limit int) []memory.SearchResult {
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

// summaryContentTime returns when a summary's underlying content actually
// occurred: latest_at (the real end of the summarized window), falling back to
// created_at (when the summary was generated) only when latest_at is null.
func summaryContentTime(latestAt pgtype.Timestamptz, createdAt time.Time) time.Time {
	if t := parseNullTime(latestAt); t != nil {
		return *t
	}
	return createdAt.UTC()
}

// searchSnippet prefers the pg_search snippet (match context with <b></b>
// highlights) and falls back to a plain truncation for degenerate empty snippets.
func searchSnippet(snippet, content string) string {
	if snippet != "" {
		return snippet
	}
	return truncateUTF8(content, maxContentSnippet)
}

// GetMessage implements memory.MessageReader.
func (p *Provider) GetMessage(ctx context.Context, messageID string) (*memory.MessageDetail, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	row, err := p.q.GetMessageScoped(ctx, sqlc.GetMessageScopedParams{
		ID:      messageID,
		UserID:  pgtype.Text{String: userID, Valid: true},
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return &memory.MessageDetail{
		MessageID:         row.ID,
		Role:              row.Role,
		Content:           row.Content,
		OccurredAt:        row.CreatedAt.UTC(),
		SessionID:         row.SessionID,
		ConversationTitle: row.ConversationTitle.String,
	}, nil
}

func (p *Provider) getScopedSummary(ctx context.Context, summaryID string) (sqlc.CtxSummary, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return sqlc.CtxSummary{}, err
	}
	sum, err := p.q.GetSummaryByID(ctx, summaryID)
	if err != nil {
		return sqlc.CtxSummary{}, fmt.Errorf("get summary: %w", err)
	}
	if _, err := p.q.GetConversation(ctx, sqlc.GetConversationParams{
		ID:      sum.ConversationID,
		UserID:  pgtype.Text{String: userID, Valid: true},
		AgentID: pgnull.Text(agentID),
	}); err != nil {
		return sqlc.CtxSummary{}, fmt.Errorf("get summary conversation: %w", err)
	}
	return sum, nil
}

// Describe implements memory.Explorer.
func (p *Provider) Describe(ctx context.Context, summaryID string) (*memory.DescribeResult, error) {
	sum, err := p.getScopedSummary(ctx, summaryID)
	if err != nil {
		return nil, err
	}
	return p.retrieval.describe(ctx, sum)
}

func (r *retrievalEngine) describe(ctx context.Context, sum sqlc.CtxSummary) (*memory.DescribeResult, error) {
	// Compaction stores (summary_id=container,
	// parent_summary_id=constituent), so these legacy query names are inverted
	// relative to the conceptual Explorer API.
	parents, err := r.q.GetSummaryChildren(ctx, sum.ID)
	if err != nil {
		return nil, fmt.Errorf("get parents: %w", err)
	}
	for _, parent := range parents {
		if parent.ConversationID != sum.ConversationID {
			return nil, fmt.Errorf("get parents: summary lineage crosses conversation boundary")
		}
	}
	parentIDs := make([]string, len(parents))
	for i, p := range parents {
		parentIDs[i] = p.ID
	}

	children, err := r.q.GetSummaryParents(ctx, sum.ID)
	if err != nil {
		return nil, fmt.Errorf("get children: %w", err)
	}
	for _, child := range children {
		if child.ConversationID != sum.ConversationID {
			return nil, fmt.Errorf("get children: summary lineage crosses conversation boundary")
		}
	}
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}

	return &memory.DescribeResult{
		SummaryID:       sum.ID,
		Kind:            sum.Kind,
		Depth:           int(sum.Depth),
		Content:         sum.Content,
		EarliestAt:      parseNullTime(sum.EarliestAt),
		LatestAt:        parseNullTime(sum.LatestAt),
		DescendantCount: int(sum.DescendantCount),
		ParentIDs:       parentIDs,
		ChildIDs:        childIDs,
	}, nil
}

// Expand implements memory.Explorer.
func (p *Provider) Expand(ctx context.Context, summaryID string, tokenCap int) (*memory.ExpandResult, error) {
	sum, err := p.getScopedSummary(ctx, summaryID)
	if err != nil {
		return nil, err
	}
	return p.retrieval.expand(ctx, sum, tokenCap)
}

func (r *retrievalEngine) expand(ctx context.Context, sum sqlc.CtxSummary, tokenCap int) (*memory.ExpandResult, error) {
	if tokenCap <= 0 {
		tokenCap = defaultExpandTokens
	}

	result := &memory.ExpandResult{SummaryID: sum.ID}
	tokensUsed := 0

	if sum.Kind == kindLeaf {
		msgs, err := r.q.GetSummaryMessages(ctx, sum.ID)
		if err != nil {
			return nil, fmt.Errorf("get summary messages: %w", err)
		}
		for _, msg := range msgs {
			if msg.ConversationID != sum.ConversationID {
				return nil, fmt.Errorf("get summary messages: summary message crosses conversation boundary")
			}
		}
		for _, msg := range msgs {
			tokens := memory.EstimateTokens(msg.Content)
			if tokensUsed+tokens > tokenCap && len(result.Messages) > 0 {
				break
			}
			result.Messages = append(result.Messages, memory.ExpandMessage{
				MessageID: msg.ID,
				Role:      msg.Role,
				Content:   msg.Content,
				CreatedAt: msg.CreatedAt.UTC(),
			})
			tokensUsed += tokens
		}
	} else {
		// GetSummaryParents returns this condensed summary's constituents; see
		// describe for the legacy relation-name explanation.
		children, err := r.q.GetSummaryParents(ctx, sum.ID)
		if err != nil {
			return nil, fmt.Errorf("get children: %w", err)
		}
		for _, child := range children {
			if child.ConversationID != sum.ConversationID {
				return nil, fmt.Errorf("get children: summary lineage crosses conversation boundary")
			}
		}
		for _, child := range children {
			tokens := memory.EstimateTokens(child.Content)
			if tokensUsed+tokens > tokenCap && len(result.Children) > 0 {
				break
			}
			result.Children = append(result.Children, memory.ExpandChild{
				SummaryID: child.ID,
				Kind:      child.Kind,
				Depth:     int(child.Depth),
				Content:   child.Content,
			})
			tokensUsed += tokens
		}
	}

	return result, nil
}

// normalizeQuery folds punctuation and symbols to spaces and collapses runs of
// whitespace. jieba emits punctuation and whitespace as their own tokens, so a
// raw query like "alpha, beaver" would have the "," token match every row that
// contains a comma; folding it to "alpha beaver" leaves only real search terms
// (index-side whitespace stopwords then drop the spaces). A query with no
// letters or digits normalizes to "", which callers treat as a no-op search.
func normalizeQuery(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// truncateUTF8 truncates s to at most maxLen runes, appending "..." if truncated.
func truncateUTF8(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}
