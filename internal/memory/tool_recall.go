package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/searchrank"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultUnifiedSearchLimit  = 20
	maxUnifiedSearchLimit      = 50
	maxUnifiedSearchWindow     = 100
	defaultUnifiedReadTokenCap = 4_000
	maxUnifiedReadTokenCap     = 8_000
	maxUnifiedReadTextBytes    = 64_000
	maxUnifiedSearchSnippet    = 1_000
	maxUnifiedProvenanceTitle  = 1_000
	maxUnifiedConstraintCount  = 100
	maxUnifiedConstraintText   = 4_000
	maxUnifiedSummaryRefCount  = 200
	maxUnifiedSummaryRefBytes  = 16_000
	maxUnifiedExpansionCount   = 200
	maxUnifiedExpansionBytes   = 40_000
	maxUnifiedVersionTextBytes = 64_000
	maxUnifiedSerializedResult = 128_000
	maxMemoryRefBytes          = 4_096
	memoryRefPrefix            = "mem1."
)

const (
	wellKnownProfile         = "profile"
	wellKnownSoul            = "soul"
	wellKnownConstraints     = "constraints"
	wellKnownProfileVersions = "profile_versions"
	wellKnownSoulVersions    = "soul_versions"
)

type memoryRefPayload struct {
	Version   int    `json:"v"`
	Kind      string `json:"k"`
	ID        string `json:"i"`
	SessionID string `json:"s,omitempty"`
}

type unifiedSearchResult struct {
	Ref              string                   `json:"ref"`
	Snippet          string                   `json:"snippet"`
	Score            float64                  `json:"score"`
	OccurredAt       string                   `json:"occurred_at,omitempty"`
	ActorType        string                   `json:"actor_type,omitempty"`
	ActorDisplayName string                   `json:"actor_display_name,omitempty"`
	Authority        string                   `json:"authority,omitempty"`
	Provenance       *unifiedRecallProvenance `json:"provenance,omitempty"`
}

type unifiedRecallProvenance struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
}

type unifiedCandidate struct {
	result     unifiedSearchResult
	factID     string
	factSource ChangeSource
}

type unifiedDurableItem struct {
	id         string
	ref        string
	content    string
	occurredAt time.Time
	factID     string
	factSource ChangeSource
}

type unifiedDurableState struct {
	profile     string
	soul        string
	constraints []ConstraintEntry
	facts       []Fact
}

type unifiedReadResponse struct {
	Ref         string                   `json:"ref"`
	Content     string                   `json:"content,omitempty"`
	Truncated   bool                     `json:"truncated,omitempty"`
	Role        string                   `json:"role,omitempty"`
	Authority   string                   `json:"authority,omitempty"`
	OccurredAt  string                   `json:"occurred_at,omitempty"`
	Provenance  *unifiedRecallProvenance `json:"provenance,omitempty"`
	Constraints []ConstraintEntry        `json:"constraints,omitempty"`
	Versions    []unifiedVersion         `json:"versions,omitempty"`
	Summary     *unifiedSummaryRead      `json:"summary,omitempty"`
	Messages    []unifiedRecallMessage   `json:"messages,omitempty"`
}

type unifiedRecallMessage struct {
	Content          string `json:"content"`
	ActorType        string `json:"actor_type"`
	ActorDisplayName string `json:"actor_display_name,omitempty"`
	Authority        string `json:"authority"`
	OccurredAt       string `json:"occurred_at,omitempty"`
	Anchor           bool   `json:"anchor,omitempty"`
	Truncated        bool   `json:"truncated,omitempty"`
}

type unifiedVersion struct {
	Version    int64        `json:"version"`
	Action     string       `json:"action"`
	Source     ChangeSource `json:"source"`
	BeforeText string       `json:"before_text,omitempty"`
	AfterText  string       `json:"after_text,omitempty"`
	CreatedAt  string       `json:"created_at"`
}

type unifiedSummaryRead struct {
	Kind            string                `json:"kind"`
	Depth           int                   `json:"depth"`
	DescendantCount int                   `json:"descendant_count"`
	EarliestAt      string                `json:"earliest_at,omitempty"`
	LatestAt        string                `json:"latest_at,omitempty"`
	ParentRefs      []string              `json:"parent_refs"`
	ChildRefs       []string              `json:"child_refs"`
	Expanded        []unifiedExpandedRead `json:"expanded"`
	Truncated       bool                  `json:"truncated,omitempty"`
}

type unifiedExpandedRead struct {
	Ref        string `json:"ref"`
	Role       string `json:"role,omitempty"`
	Authority  string `json:"authority,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
	Content    string `json:"content"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

func (t *memoryTool) execUnifiedSearch(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("memory search: query is required")
	}
	authority, err := authz.ToolAuthority(ctx, "memory")
	if err != nil {
		return "", err
	}
	agentID := string(authority.AgentID())
	limit := intArg(args, "limit", defaultUnifiedSearchLimit)
	if limit <= 0 {
		limit = defaultUnifiedSearchLimit
	}
	limit = min(limit, maxUnifiedSearchLimit)

	recallLimit := maxUnifiedSearchWindow
	if authority.Kind() == authz.ActorGroupAgent {
		recallLimit = limit
	}
	recallHits, err := t.cfg.recallSource.SearchRecall(ctx, authority, agentID, query, recallLimit)
	if err != nil {
		return "", fmt.Errorf("memory search: conversation recall: %w", err)
	}
	recallLane := make([]unifiedCandidate, 0, len(recallHits))
	for _, hit := range recallHits {
		ref, err := encodeMemoryRef(memoryRefPayload{
			Version: 1, Kind: hit.Reference.Kind, ID: hit.Reference.ID, SessionID: hit.Reference.SessionID,
		})
		if err != nil {
			return "", fmt.Errorf("memory search: encode ref: %w", err)
		}
		snippet := strings.ReplaceAll(strings.ReplaceAll(hit.Content, "<b>", ""), "</b>", "")
		snippet, _ = tools.TruncateText(snippet, maxUnifiedSearchSnippet)
		result := unifiedSearchResult{
			Ref: ref, Snippet: snippet, Score: hit.Score, ActorType: hit.ActorType,
			ActorDisplayName: truncateUnifiedText(hit.ActorDisplayName, 256), Authority: hit.Authority,
		}
		if hit.SessionID != "" {
			result.Provenance = &unifiedRecallProvenance{SessionID: hit.SessionID, Title: truncateUnifiedText(hit.ConversationTitle, maxUnifiedProvenanceTitle)}
		}
		if !hit.OccurredAt.IsZero() {
			result.OccurredAt = hit.OccurredAt.UTC().Format(time.RFC3339)
		}
		recallLane = append(recallLane, unifiedCandidate{result: result})
	}
	if authority.Kind() == authz.ActorGroupAgent {
		results := make([]unifiedSearchResult, len(recallLane))
		for i, candidate := range recallLane {
			results[i] = candidate.result
		}
		return marshalUnifiedJSON(map[string]any{"results": results})
	}

	userID := string(authority.UserID())
	state, err := t.loadUnifiedDurableState(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	durableLane, err := rankUnifiedDurable(query, state)
	if err != nil {
		return "", fmt.Errorf("memory search: durable memory: %w", err)
	}
	merged := mergeUnifiedSearchLanes(limit, recallLane, durableLane)
	if len(merged) == 0 {
		return marshalUnifiedJSON(map[string]any{"results": []unifiedSearchResult{}})
	}
	results := make([]unifiedSearchResult, len(merged))
	returnedFacts := make([]knowledgeSearchResult, 0, len(merged))
	for i, candidate := range merged {
		results[i] = candidate.result
		if candidate.factID != "" {
			returnedFacts = append(returnedFacts, knowledgeSearchResult{FactID: candidate.factID, Source: candidate.factSource})
		}
	}
	t.touchReturnedKnowledgeUsage(ctx, userID, agentID, returnedFacts)
	return marshalUnifiedJSON(map[string]any{"results": results})
}

func rankUnifiedDurable(query string, state unifiedDurableState) ([]unifiedCandidate, error) {
	items := make([]unifiedDurableItem, 0, len(state.facts)+3)
	if strings.TrimSpace(state.profile) != "" {
		items = append(items, unifiedDurableItem{id: wellKnownProfile, ref: wellKnownProfile, content: state.profile})
	}
	if strings.TrimSpace(state.soul) != "" {
		items = append(items, unifiedDurableItem{id: wellKnownSoul, ref: wellKnownSoul, content: state.soul})
	}
	if len(state.constraints) > 0 {
		lines := make([]string, 0, len(state.constraints))
		for _, entry := range state.constraints {
			lines = append(lines, entry.Text)
		}
		items = append(items, unifiedDurableItem{id: wellKnownConstraints, ref: wellKnownConstraints, content: strings.Join(lines, "\n")})
	}
	for _, fact := range state.facts {
		ref, err := encodeMemoryRef(memoryRefPayload{Version: 1, Kind: "fact", ID: fact.ID})
		if err != nil {
			return nil, err
		}
		items = append(items, unifiedDurableItem{
			id: "fact:" + fact.ID, ref: ref, content: fact.Content, occurredAt: fact.UpdatedAt,
			factID: fact.ID, factSource: fact.Source,
		})
	}
	docs := make([]searchrank.Document, 0, len(items))
	byID := make(map[string]unifiedDurableItem, len(items))
	for _, item := range items {
		byID[item.id] = item
		docs = append(docs, searchrank.Document{ID: item.id, Fields: []searchrank.Field{{Name: "content", Text: item.content, Weight: 1}}})
	}
	ranked := searchrank.Rank(query, docs, maxUnifiedSearchWindow)
	out := make([]unifiedCandidate, 0, len(ranked))
	for _, hit := range ranked {
		item := byID[hit.ID]
		snippet, _ := tools.TruncateText(hit.Snippet, maxUnifiedSearchSnippet)
		result := unifiedSearchResult{Ref: item.ref, Snippet: snippet}
		if !item.occurredAt.IsZero() {
			result.OccurredAt = item.occurredAt.UTC().Format(time.RFC3339)
		}
		out = append(out, unifiedCandidate{result: result, factID: item.factID, factSource: item.factSource})
	}
	return out, nil
}

func mergeUnifiedSearchLanes(limit int, lanes ...[]unifiedCandidate) []unifiedCandidate {
	const rrfK = 60.0
	merged := make([]unifiedCandidate, 0)
	for _, lane := range lanes {
		for rank, candidate := range lane {
			candidate.result.Score = 1 / (rrfK + float64(rank+1))
			merged = append(merged, candidate)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].result.Score != merged[j].result.Score {
			return merged[i].result.Score > merged[j].result.Score
		}
		return merged[i].result.Ref < merged[j].result.Ref
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func (t *memoryTool) execUnifiedRead(ctx context.Context, args map[string]any) (string, error) {
	ref, _ := args["ref"].(string)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("memory read: ref is required")
	}
	tokenCap := intArg(args, "token_cap", defaultUnifiedReadTokenCap)
	if tokenCap <= 0 {
		tokenCap = defaultUnifiedReadTokenCap
	}
	tokenCap = min(tokenCap, maxUnifiedReadTokenCap)
	authority, err := authz.ToolAuthority(ctx, "memory")
	if err != nil {
		return "", err
	}
	if authority.Kind() == authz.ActorGroupAgent {
		payload, err := decodeMemoryRef(ref)
		if err != nil || payload.Kind != "group_message" {
			return "", fmt.Errorf("memory read: ref not found")
		}
		return t.readUnifiedRecall(ctx, ref, payload, tokenCap, authority)
	}

	switch ref {
	case wellKnownProfile:
		return t.readUnifiedProfile(ctx, ref, false)
	case wellKnownSoul:
		return t.readUnifiedProfile(ctx, ref, true)
	case wellKnownConstraints:
		return t.readUnifiedConstraints(ctx, ref)
	case wellKnownProfileVersions:
		return t.readUnifiedVersions(ctx, ref, "profile")
	case wellKnownSoulVersions:
		return t.readUnifiedVersions(ctx, ref, "soul")
	}

	payload, err := decodeMemoryRef(ref)
	if err != nil {
		return "", fmt.Errorf("memory read: invalid ref")
	}
	switch payload.Kind {
	case "fact":
		return t.readUnifiedFact(ctx, ref, payload.ID)
	case "message", "summary":
		return t.readUnifiedRecall(ctx, ref, payload, tokenCap, authority)
	default:
		return "", fmt.Errorf("memory read: invalid ref")
	}
}

func (t *memoryTool) readUnifiedRecall(ctx context.Context, encoded string, payload memoryRefPayload, tokenCap int, authority authz.Authority) (string, error) {
	doc, err := t.cfg.recallSource.ReadRecall(ctx, authority, string(authority.AgentID()), RecallReference{
		Kind: payload.Kind, ID: payload.ID, SessionID: payload.SessionID,
	}, tokenCap)
	if err != nil {
		return "", fmt.Errorf("memory read: %w", err)
	}
	content, contentTruncated := tools.TruncateText(doc.Content, maxUnifiedReadTextBytes)
	response := unifiedReadResponse{
		Ref: encoded, Content: content, Truncated: doc.Truncated || contentTruncated, Role: doc.Role, Authority: doc.Authority,
	}
	if doc.SessionID != "" {
		response.Provenance = &unifiedRecallProvenance{SessionID: doc.SessionID, Title: truncateUnifiedText(doc.ConversationTitle, maxUnifiedProvenanceTitle)}
	}
	if !doc.OccurredAt.IsZero() {
		response.OccurredAt = doc.OccurredAt.UTC().Format(time.RFC3339)
	}
	if doc.Summary != nil {
		summary, err := unifiedSummaryFrom(doc.Summary)
		if err != nil {
			return "", fmt.Errorf("memory read: encode summary refs: %w", err)
		}
		response.Summary = summary
	}
	if len(doc.Messages) > 0 {
		response.Content = ""
		response.Messages = make([]unifiedRecallMessage, 0, len(doc.Messages))
		for _, message := range doc.Messages {
			item := unifiedRecallMessage{
				Content: message.Content, ActorType: message.ActorType,
				ActorDisplayName: truncateUnifiedText(message.ActorDisplayName, 256), Authority: message.Authority,
				Anchor: message.Anchor, Truncated: message.Truncated,
			}
			if !message.OccurredAt.IsZero() {
				item.OccurredAt = message.OccurredAt.UTC().Format(time.RFC3339)
			}
			response.Messages = append(response.Messages, item)
		}
	}
	return marshalUnifiedJSON(response)
}

func unifiedSummaryFrom(detail *RecallSummaryDetail) (*unifiedSummaryRead, error) {
	out := &unifiedSummaryRead{
		Kind: detail.Kind, Depth: detail.Depth, DescendantCount: detail.DescendantCount,
		ParentRefs: make([]string, 0, len(detail.Parents)), ChildRefs: make([]string, 0, len(detail.Children)),
		Expanded: make([]unifiedExpandedRead, 0, len(detail.Expanded)),
	}
	if detail.EarliestAt != nil {
		out.EarliestAt = detail.EarliestAt.UTC().Format(time.RFC3339)
	}
	if detail.LatestAt != nil {
		out.LatestAt = detail.LatestAt.UTC().Format(time.RFC3339)
	}
	refBytes := 0
	refCount := 0
	appendRef := func(target *[]string, ref RecallReference) error {
		if refCount == maxUnifiedSummaryRefCount {
			out.Truncated = true
			return nil
		}
		encoded, err := encodeRecallReference(ref)
		if err != nil {
			return err
		}
		if refBytes+len(encoded) > maxUnifiedSummaryRefBytes {
			out.Truncated = true
			return nil
		}
		*target = append(*target, encoded)
		refCount++
		refBytes += len(encoded)
		return nil
	}
	for _, ref := range detail.Parents {
		if err := appendRef(&out.ParentRefs, ref); err != nil {
			return nil, err
		}
	}
	for _, ref := range detail.Children {
		if err := appendRef(&out.ChildRefs, ref); err != nil {
			return nil, err
		}
	}
	expansionBytes := 0
	for _, item := range detail.Expanded {
		if len(out.Expanded) == maxUnifiedExpansionCount || expansionBytes == maxUnifiedExpansionBytes {
			out.Truncated = true
			break
		}
		encoded, err := encodeRecallReference(item.Reference)
		if err != nil {
			return nil, err
		}
		remaining := maxUnifiedExpansionBytes - expansionBytes
		content, truncated := tools.TruncateText(item.Content, remaining)
		expanded := unifiedExpandedRead{
			Ref: encoded, Role: item.Role, Authority: item.Authority, Kind: item.Kind, Depth: item.Depth,
			Content: content, Truncated: truncated,
		}
		expansionBytes += len(content)
		out.Truncated = out.Truncated || truncated
		if !item.OccurredAt.IsZero() {
			expanded.OccurredAt = item.OccurredAt.UTC().Format(time.RFC3339)
		}
		out.Expanded = append(out.Expanded, expanded)
	}
	return out, nil
}

func (t *memoryTool) readUnifiedProfile(ctx context.Context, ref string, soul bool) (string, error) {
	userID, agentID, err := t.requireProfileCtx(ctx, actionRead)
	if err != nil {
		return "", err
	}
	version, frozen, err := t.unifiedSnapshot(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	var content string
	if frozen && t.versionedProfiles == nil {
		return "", fmt.Errorf("memory read: snapshot profile reads are not supported by provider")
	}
	switch {
	case frozen && soul:
		content, err = t.versionedProfiles.GetAgentSoulAt(ctx, userID, agentID, version)
	case frozen:
		content, err = t.versionedProfiles.GetProfileAt(ctx, userID, agentID, version)
	case soul:
		content, err = t.profileStore.GetAgentSoul(ctx, userID, agentID)
	default:
		content, err = t.profileStore.GetProfile(ctx, userID, agentID)
	}
	if err != nil {
		return "", fmt.Errorf("memory read: %w", err)
	}
	content, truncated := tools.TruncateText(content, maxUnifiedReadTextBytes)
	return marshalUnifiedJSON(unifiedReadResponse{Ref: ref, Content: content, Truncated: truncated})
}

func (t *memoryTool) readUnifiedConstraints(ctx context.Context, ref string) (string, error) {
	userID, agentID, err := t.requireConstraintCtx(ctx, actionRead)
	if err != nil {
		return "", err
	}
	version, frozen, err := t.unifiedSnapshot(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	var entries []ConstraintEntry
	if frozen {
		if t.versionedConstraints == nil {
			return "", fmt.Errorf("memory read: snapshot constraint reads are not supported by provider")
		}
		entries, err = t.versionedConstraints.GetConstraintsAt(ctx, userID, agentID, version)
	} else {
		entries, err = t.constraintStore.GetConstraints(ctx, userID, agentID)
	}
	if err != nil {
		return "", fmt.Errorf("memory read: %w", err)
	}
	entries, truncated := boundUnifiedConstraints(entries)
	return marshalUnifiedJSON(unifiedReadResponse{Ref: ref, Constraints: entries, Truncated: truncated})
}

func (t *memoryTool) readUnifiedFact(ctx context.Context, ref, factID string) (string, error) {
	userID, agentID, err := t.requireKnowledgeCtx(ctx, actionRead)
	if err != nil {
		return "", err
	}
	facts, err := t.searchKnowledgeFacts(ctx, userID, agentID, actionRead)
	if err != nil {
		return "", err
	}
	for _, fact := range facts {
		if fact.ID != factID {
			continue
		}
		content, truncated := tools.TruncateText(fact.Content, maxUnifiedReadTextBytes)
		response := unifiedReadResponse{Ref: ref, Content: content, Truncated: truncated}
		if !fact.UpdatedAt.IsZero() {
			response.OccurredAt = fact.UpdatedAt.UTC().Format(time.RFC3339)
		}
		t.touchReturnedKnowledgeUsage(ctx, userID, agentID, []knowledgeSearchResult{{FactID: fact.ID, Source: fact.Source}})
		return marshalUnifiedJSON(response)
	}
	return "", fmt.Errorf("memory read: ref not found")
}

func (t *memoryTool) readUnifiedVersions(ctx context.Context, ref, scope string) (string, error) {
	if t.changelogReader == nil {
		return "", fmt.Errorf("memory read: version history is not supported by provider")
	}
	userID, agentID, err := t.requireProfileCtx(ctx, actionRead)
	if err != nil {
		return "", err
	}
	version, frozen, err := t.unifiedSnapshot(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	entries, err := t.changelogReader.ReadChangelog(ctx, userID, agentID, scope, 100)
	if err != nil {
		return "", fmt.Errorf("memory read: version history: %w", err)
	}
	versions := make([]unifiedVersion, 0, min(20, len(entries)))
	remainingText := maxUnifiedVersionTextBytes
	truncated := false
	for _, entry := range entries {
		entryVersion := int64(0)
		if entry.MemoryVersionAfter != nil {
			entryVersion = *entry.MemoryVersionAfter
		}
		if frozen && (entry.MemoryVersionAfter == nil || entryVersion > version) {
			continue
		}
		if remainingText == 0 {
			truncated = true
			break
		}
		beforeLimit := min(4_000, remainingText)
		before, beforeTruncated := tools.TruncateText(entry.BeforeText, beforeLimit)
		remainingText -= len(before)
		afterLimit := min(4_000, remainingText)
		after, afterTruncated := tools.TruncateText(entry.AfterText, afterLimit)
		remainingText -= len(after)
		truncated = truncated || beforeTruncated || afterTruncated
		versions = append(versions, unifiedVersion{
			Version: entryVersion, Action: entry.Action, Source: entry.Source,
			BeforeText: before, AfterText: after, CreatedAt: entry.CreatedAt,
		})
		if len(versions) == 20 {
			truncated = true
			break
		}
	}
	return marshalUnifiedJSON(unifiedReadResponse{Ref: ref, Versions: versions, Truncated: truncated})
}

func (t *memoryTool) loadUnifiedDurableState(ctx context.Context, userID, agentID string) (unifiedDurableState, error) {
	version, frozen, err := t.unifiedSnapshot(ctx, userID, agentID)
	if err != nil {
		return unifiedDurableState{}, err
	}
	var state unifiedDurableState
	if t.profileStore != nil {
		if frozen {
			if t.versionedProfiles == nil {
				return state, fmt.Errorf("memory search: snapshot profile reads are not supported by provider")
			}
			state.profile, err = t.versionedProfiles.GetProfileAt(ctx, userID, agentID, version)
			if err == nil {
				state.soul, err = t.versionedProfiles.GetAgentSoulAt(ctx, userID, agentID, version)
			}
		} else {
			state.profile, err = t.profileStore.GetProfile(ctx, userID, agentID)
			if err == nil {
				state.soul, err = t.profileStore.GetAgentSoul(ctx, userID, agentID)
			}
		}
		if err != nil {
			return state, fmt.Errorf("memory search: load profile and soul: %w", err)
		}
	}
	if t.constraintStore != nil {
		if frozen {
			if t.versionedConstraints == nil {
				return state, fmt.Errorf("memory search: snapshot constraint reads are not supported by provider")
			}
			state.constraints, err = t.versionedConstraints.GetConstraintsAt(ctx, userID, agentID, version)
		} else {
			state.constraints, err = t.constraintStore.GetConstraints(ctx, userID, agentID)
		}
		if err != nil {
			return state, fmt.Errorf("memory search: load constraints: %w", err)
		}
	}
	if t.factStore != nil {
		if frozen {
			if t.versionedFacts == nil {
				return state, fmt.Errorf("memory search: snapshot facts are not supported by provider")
			}
			state.facts, err = t.versionedFacts.ListActiveFactsAt(ctx, userID, agentID, FactSubjectWorld, version)
		} else {
			state.facts, err = t.factStore.ListActiveFacts(ctx, userID, agentID, FactSubjectWorld)
		}
		if err != nil {
			return state, fmt.Errorf("memory search: load facts: %w", err)
		}
		state.facts = filterWorldKnowledgeFacts(state.facts)
	}
	return state, nil
}

func (t *memoryTool) unifiedSnapshot(ctx context.Context, userID, agentID string) (version int64, frozen bool, err error) {
	if t.snapshotStore == nil || SessionIDFromContext(ctx) == "" {
		return 0, false, nil
	}
	snapshot, err := t.snapshotStore.GetOrCreateSessionSnapshot(ctx, SessionIDFromContext(ctx), userID, agentID)
	if err != nil {
		return 0, false, fmt.Errorf("memory: get session snapshot: %w", err)
	}
	return snapshot.Version, true, nil
}

func encodeRecallReference(ref RecallReference) (string, error) {
	return encodeMemoryRef(memoryRefPayload{Version: 1, Kind: ref.Kind, ID: ref.ID, SessionID: ref.SessionID})
}

func encodeMemoryRef(payload memoryRefPayload) (string, error) {
	if payload.Version != 1 || payload.ID == "" {
		return "", fmt.Errorf("invalid memory ref")
	}
	switch payload.Kind {
	case "message", "summary":
		if payload.SessionID == "" {
			return "", fmt.Errorf("invalid conversation memory ref")
		}
	case "fact":
		if payload.SessionID != "" {
			return "", fmt.Errorf("invalid durable memory ref")
		}
	case "group_message":
		if payload.SessionID != "" {
			return "", fmt.Errorf("invalid group message recall ref")
		}
	default:
		return "", fmt.Errorf("invalid memory ref kind")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := memoryRefPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > maxMemoryRefBytes {
		return "", fmt.Errorf("memory ref is too large")
	}
	return encoded, nil
}

func decodeMemoryRef(ref string) (memoryRefPayload, error) {
	if len(ref) > maxMemoryRefBytes || !strings.HasPrefix(ref, memoryRefPrefix) {
		return memoryRefPayload{}, fmt.Errorf("invalid memory ref")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, memoryRefPrefix))
	if err != nil {
		return memoryRefPayload{}, err
	}
	var payload memoryRefPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return memoryRefPayload{}, err
	}
	if _, err := encodeMemoryRef(payload); err != nil {
		return memoryRefPayload{}, err
	}
	return payload, nil
}

func boundUnifiedConstraints(entries []ConstraintEntry) ([]ConstraintEntry, bool) {
	out := make([]ConstraintEntry, 0, min(len(entries), maxUnifiedConstraintCount))
	remaining := maxUnifiedReadTextBytes
	truncated := false
	for _, entry := range entries {
		if len(out) == maxUnifiedConstraintCount || remaining == 0 {
			return out, true
		}
		limit := min(maxUnifiedConstraintText, remaining)
		var itemTruncated bool
		entry.Text, itemTruncated = tools.TruncateText(entry.Text, limit)
		remaining -= len(entry.Text)
		out = append(out, entry)
		truncated = truncated || itemTruncated
	}
	return out, truncated
}

func truncateUnifiedText(value string, limit int) string {
	value, _ = tools.TruncateText(value, limit)
	return value
}

func marshalUnifiedJSON(value any) (string, error) {
	result, err := marshalJSON(value)
	if err == nil && len(result) > maxUnifiedSerializedResult {
		return "", fmt.Errorf("memory result exceeded its serialized limit")
	}
	return result, err
}
