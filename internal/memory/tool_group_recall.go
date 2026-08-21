package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
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

// execGroupRecall is the group-authority action surface. Execute reaches it
// before validating normal actions or interpreting a ref, so group turns never
// enter the private unified-memory dispatch.
func (t *memoryTool) execGroupRecall(ctx context.Context, args map[string]any) (string, error) {
	// The group context is written by the trusted runtime together with the
	// trigger sequence. This tool is not an Authority minting boundary.
	groupID := authz.GroupIDFromContext(ctx)
	triggerSeq := GroupSeqFromContext(ctx)
	if groupID == "" || triggerSeq <= 0 || t.cfg.groupRecallSource == nil {
		return "", fmt.Errorf("memory group recall: unavailable")
	}

	action, _ := args["action"].(string)
	switch action {
	case actionSearch:
		return t.execGroupRecallSearch(ctx, groupID, triggerSeq, args)
	case actionRead:
		return t.execGroupRecallRead(ctx, groupID, triggerSeq, args)
	default:
		return "", fmt.Errorf("memory group recall: action not found")
	}
}

func (t *memoryTool) execGroupRecallSearch(ctx context.Context, groupID string, triggerSeq int64, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("memory search: query is required")
	}
	limit := intArg(args, "limit", groupRecallDefaultSearchLimit)
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

func (t *memoryTool) execGroupRecallRead(ctx context.Context, groupID string, triggerSeq int64, args map[string]any) (string, error) {
	ref, _ := args["ref"].(string)
	payload, err := decodeMemoryRef(strings.TrimSpace(ref))
	if err != nil || payload.Kind != "group_message" || payload.SessionID != "" {
		return "", fmt.Errorf("memory read: ref not found")
	}
	tokenCap := intArg(args, "token_cap", defaultUnifiedReadTokenCap)
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
	return fmt.Sprintf("[seq:%d %s]: %s", row.Seq, name, row.Content)
}
