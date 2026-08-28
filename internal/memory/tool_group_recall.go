package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/grouptranscript"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	groupRecallDefaultSearchLimit = 20
	groupRecallMaxSearchLimit     = 50
	groupRecallMaxReadMessages    = 200
)

type groupRecallSearchResult struct {
	Ref         string  `json:"ref"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
	OccurredAt  string  `json:"occurred_at"`
	ActorType   string  `json:"actor_type"`
	DisplayName string  `json:"display_name,omitempty"`
	Authority   string  `json:"authority"`
}

type groupRecallFragment struct {
	Content    string `json:"content"`
	OccurredAt string `json:"occurred_at"`
	Anchor     bool   `json:"anchor,omitempty"`
	Authority  string `json:"authority"`
}

type groupRecallReadResponse struct {
	Ref       string                `json:"ref"`
	Messages  []groupRecallFragment `json:"messages"`
	Truncated bool                  `json:"truncated,omitempty"`
}

// execGroupRecall is the retired union's group entry. Execute reaches it before
// validating normal actions or interpreting a ref, so group turns never enter
// the private unified-memory dispatch. The split tools route by name instead.
func (t *memoryTool) execGroupRecall(ctx context.Context, args map[string]any) (string, error) {
	if _, _, err := t.groupLane(ctx); err != nil {
		return "", err
	}
	action, _ := args["action"].(string)
	switch action {
	case actionSearch:
		query, _ := args["query"].(string)
		return t.groupSearch(ctx, MemorySearchInput{Q: query, Limit: intArg(args, "limit", 0)})
	case actionRead:
		ref, _ := args["ref"].(string)
		return t.groupRead(ctx, MemoryReadInput{Ref: ref, TokenCap: intArg(args, "token_cap", 0)})
	default:
		return "", fmt.Errorf("memory group recall: action not found")
	}
}

// groupLane resolves the group this turn may recall from. The group context is
// written by the trusted runtime together with the trigger sequence; this tool
// is not an Authority minting boundary.
func (t *memoryTool) groupLane(ctx context.Context) (string, int64, error) {
	groupID := authz.GroupIDFromContext(ctx)
	triggerSeq := GroupSeqFromContext(ctx)
	if groupID == "" || triggerSeq <= 0 || t.cfg.groupRecallSource == nil {
		return "", 0, fmt.Errorf("memory group recall: unavailable")
	}
	return groupID, triggerSeq, nil
}

func (t *memoryTool) groupSearch(ctx context.Context, in MemorySearchInput) (string, error) {
	groupID, triggerSeq, err := t.groupLane(ctx)
	if err != nil {
		return "", err
	}
	query := in.Q
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("memory search: query is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = groupRecallDefaultSearchLimit
	}
	limit = min(limit, groupRecallMaxSearchLimit)
	rows, err := t.cfg.groupRecallSource.SearchGroupRecall(ctx, groupID, triggerSeq, query, limit)
	if err != nil {
		return "", fmt.Errorf("memory search: group history: %w", err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]groupRecallSearchResult, 0, len(rows))
	for _, row := range rows {
		ref, err := encodeMemoryRef(memoryRefPayload{Version: 1, Kind: "group_message", ID: row.ID})
		if err != nil {
			return "", fmt.Errorf("memory search: encode ref: %w", err)
		}
		snippet, _ := tools.TruncateText(strings.ReplaceAll(strings.ReplaceAll(row.Snippet, "<b>", ""), "</b>", ""), maxUnifiedSearchSnippet)
		out = append(out, groupRecallSearchResult{
			Ref: ref, Snippet: snippet, Score: row.Score, OccurredAt: row.OccurredAt.UTC().Format(time.RFC3339),
			ActorType: row.ActorType, DisplayName: row.ActorDisplayName, Authority: "information_only",
		})
	}
	return marshalUnifiedJSON(map[string]any{"results": out})
}

func (t *memoryTool) groupRead(ctx context.Context, in MemoryReadInput) (string, error) {
	groupID, triggerSeq, err := t.groupLane(ctx)
	if err != nil {
		return "", err
	}
	ref := in.Ref
	payload, err := decodeMemoryRef(strings.TrimSpace(ref))
	if err != nil || payload.Kind != "group_message" || payload.SessionID != "" {
		return "", fmt.Errorf("memory read: ref not found")
	}
	tokenCap := in.TokenCap
	if tokenCap <= 0 {
		tokenCap = defaultUnifiedReadTokenCap
	}
	tokenCap = min(tokenCap, maxUnifiedReadTokenCap)
	rows, truncated, err := t.cfg.groupRecallSource.ReadGroupRecall(ctx, groupID, triggerSeq, payload.ID, tokenCap)
	if errors.Is(err, ErrGroupRecallNotFound) {
		return "", fmt.Errorf("memory read: ref not found")
	}
	if err != nil {
		return "", fmt.Errorf("memory read: group history: %w", err)
	}
	if len(rows) > groupRecallMaxReadMessages {
		rows = rows[:groupRecallMaxReadMessages]
		truncated = true
	}
	messages := make([]groupRecallFragment, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, groupRecallFragment{
			Content: formatGroupRecallLine(row), OccurredAt: row.OccurredAt.UTC().Format(time.RFC3339),
			Anchor: row.ID == payload.ID, Authority: "information_only",
		})
	}
	return marshalUnifiedJSON(groupRecallReadResponse{Ref: ref, Messages: messages, Truncated: truncated})
}

func formatGroupRecallLine(row GroupRecallResult) string {
	name := row.ActorDisplayName
	if name == "" {
		name = row.ActorType
	}
	return grouptranscript.RenderGroupTranscriptLine(grouptranscript.GroupTranscriptEvent{
		Seq: row.Seq, ActorType: row.ActorType, DisplayName: name, Content: row.Content,
	})
}
