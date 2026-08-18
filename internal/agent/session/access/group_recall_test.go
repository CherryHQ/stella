package access

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestGroupRecallSearchUsesPublicGroupAndTriggerBoundary(t *testing.T) {
	m := newSessionMatrix(t)
	otherGroup := uuid.NewString()
	q := sqlc.New(m.db)
	if _, err := q.CreateGroupState(t.Context(), sqlc.CreateGroupStateParams{
		ID: otherGroup, Platform: "test", PlatformGroupID: "other-group", GroupName: "other",
	}); err != nil {
		t.Fatal(err)
	}
	firstID := seedGroupRecallMessage(t, q, m.group, 1, "human", "u-1", "小明", "发布窗口 release window 定在周五晚上")
	seedGroupRecallMessage(t, q, m.group, 2, "agent", "other-agent", "Bob", "ordinary status update")
	seedGroupRecallMessage(t, q, m.group, 3, "human", "u-3", "Future", "发布窗口改到下周")
	seedGroupRecallMessage(t, q, otherGroup, 1, "human", "u-4", "Other", "发布窗口属于另一个群")

	authority, err := authz.NewGroupAgentAuthority(authz.GroupID(m.group), authz.AgentID(m.agent))
	if err != nil {
		t.Fatal(err)
	}
	ctx := memory.WithGroupSeq(t.Context(), 3)
	hits, err := m.svc.SearchRecall(ctx, authority, m.agent, "发布窗口", 20)
	if err != nil {
		t.Fatalf("SearchRecall: %v", err)
	}
	if len(hits) != 1 || hits[0].Reference.Kind != "group_message" || hits[0].ActorDisplayName != "小明" || hits[0].Authority != "information_only" {
		t.Fatalf("group hits = %#v", hits)
	}
	if !strings.Contains(hits[0].Content, "发布") || !strings.Contains(hits[0].Content, "窗口") || hits[0].Score <= 0 {
		t.Fatalf("group BM25 hit = %#v", hits[0])
	}
	for _, query := range []string{"release window", "发布 release"} {
		mixedHits, err := m.svc.SearchRecall(ctx, authority, m.agent, query, 20)
		if err != nil || len(mixedHits) == 0 || mixedHits[0].Reference.ID != firstID {
			t.Fatalf("query %q hits=%#v err=%v", query, mixedHits, err)
		}
	}

	nameHits, err := m.svc.SearchRecall(ctx, authority, m.agent, "Bob", 20)
	if err != nil || len(nameHits) != 1 || nameHits[0].ActorDisplayName != "Bob" || nameHits[0].ActorType != "agent" {
		t.Fatalf("display-name search hits=%#v err=%v", nameHits, err)
	}
	punctuation, err := m.svc.SearchRecall(ctx, authority, m.agent, "？！...", 20)
	if err != nil || len(punctuation) != 0 {
		t.Fatalf("punctuation search hits=%#v err=%v", punctuation, err)
	}
	if _, err := m.svc.SearchRecall(t.Context(), authority, m.agent, "发布窗口", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trigger boundary error = %v", err)
	}
}

func TestGroupRecallSearchHasStableRecencyTieBreak(t *testing.T) {
	m := newSessionMatrix(t)
	q := sqlc.New(m.db)
	seedGroupRecallMessage(t, q, m.group, 1, "human", "u", "Alice", "identical stable ranking text")
	newerID := seedGroupRecallMessage(t, q, m.group, 2, "human", "u", "Alice", "identical stable ranking text")
	authority, err := authz.NewGroupAgentAuthority(authz.GroupID(m.group), authz.AgentID(m.agent))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := m.svc.SearchRecall(memory.WithGroupSeq(t.Context(), 3), authority, m.agent, "identical stable ranking", 20)
	if err != nil {
		t.Fatalf("SearchRecall: %v", err)
	}
	if len(hits) != 2 || hits[0].Reference.ID != newerID {
		t.Fatalf("stable tie-break hits=%#v", hits)
	}
}

func TestGroupRecallSearchCapsDirectPEPResults(t *testing.T) {
	m := newSessionMatrix(t)
	q := sqlc.New(m.db)
	for seq := int64(1); seq <= 55; seq++ {
		seedGroupRecallMessage(t, q, m.group, seq, "human", "u", "Alice", "容量边界检索标记")
	}
	authority, err := authz.NewGroupAgentAuthority(authz.GroupID(m.group), authz.AgentID(m.agent))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := m.svc.SearchRecall(memory.WithGroupSeq(t.Context(), 100), authority, m.agent, "容量边界", 100)
	if err != nil {
		t.Fatalf("SearchRecall: %v", err)
	}
	if len(hits) != maxGroupRecallResults {
		t.Fatalf("group PEP returned %d hits, want cap %d", len(hits), maxGroupRecallResults)
	}
}

func TestGroupRecallReadReauthorizesAndReturnsChronologicalNeighborhood(t *testing.T) {
	m := newSessionMatrix(t)
	q := sqlc.New(m.db)
	otherGroup := uuid.NewString()
	if _, err := q.CreateGroupState(t.Context(), sqlc.CreateGroupStateParams{
		ID: otherGroup, Platform: "test", PlatformGroupID: "other-read-group", GroupName: "other",
	}); err != nil {
		t.Fatal(err)
	}
	otherGroupMessageID := seedGroupRecallMessage(t, q, otherGroup, 1, "human", "other-user", "Other", "private to another group")
	ids := make([]string, 0, 9)
	for seq := int64(1); seq <= 9; seq++ {
		ids = append(ids, seedGroupRecallMessage(t, q, m.group, seq, "human", "u", "Alice", strings.Repeat(string(rune('a'+seq-1)), 8)))
	}
	authority, err := authz.NewGroupAgentAuthority(authz.GroupID(m.group), authz.AgentID(m.agent))
	if err != nil {
		t.Fatal(err)
	}
	ctx := memory.WithGroupSeq(t.Context(), 9)
	doc, err := m.svc.ReadRecall(ctx, authority, m.agent, memory.RecallReference{Kind: "group_message", ID: ids[4]}, 10)
	if err != nil {
		t.Fatalf("ReadRecall: %v", err)
	}
	if len(doc.Messages) != 5 || !doc.Messages[2].Anchor {
		t.Fatalf("read neighborhood = %#v", doc.Messages)
	}
	for i, message := range doc.Messages {
		if message.Authority != "information_only" || message.ActorType != "human" || message.ActorDisplayName != "Alice" {
			t.Fatalf("message[%d] metadata = %#v", i, message)
		}
		if i > 0 && !message.OccurredAt.After(doc.Messages[i-1].OccurredAt) {
			t.Fatalf("messages are not chronological: %#v", doc.Messages)
		}
	}

	for _, ref := range []memory.RecallReference{
		{Kind: "group_message", ID: ids[8]},
		{Kind: "group_message", ID: otherGroupMessageID},
		{Kind: "group_message", ID: ids[4], SessionID: "private"},
		{Kind: "message", ID: ids[4]},
		{Kind: "group_message", ID: uuid.NewString()},
	} {
		if _, err := m.svc.ReadRecall(ctx, authority, m.agent, ref, 4_000); !errors.Is(err, ErrNotFound) {
			t.Fatalf("forged ref %#v error=%v", ref, err)
		}
	}
}

func TestPackGroupRecallMessagesTruncatesOversizedAnchorSafely(t *testing.T) {
	anchor := groupRecallMessage{id: uuid.NewString(), seq: 3, content: strings.Repeat("中文", 20), actorType: "human", occurredAt: time.Now().UTC()}
	messages, truncated := packGroupRecallMessages(anchor, nil, 4, maxGroupRecallMessages)
	if !truncated || len(messages) != 1 || !messages[0].Anchor || !messages[0].Truncated {
		t.Fatalf("oversized anchor result=%#v truncated=%t", messages, truncated)
	}
	if memory.EstimateTokens(messages[0].Content) > 4 || !utf8.ValidString(messages[0].Content) {
		t.Fatalf("anchor content bytes=%d tokens=%d valid=%t", len(messages[0].Content), memory.EstimateTokens(messages[0].Content), utf8.ValidString(messages[0].Content))
	}
}

func TestPackGroupRecallMessagesTransfersUnusedBudgetToOneSide(t *testing.T) {
	when := time.Now().UTC()
	anchor := groupRecallMessage{id: uuid.NewString(), seq: 10, content: "a", actorType: "human", occurredAt: when}
	neighbors := []groupRecallMessage{
		{id: uuid.NewString(), seq: 11, content: "bbbbbbbb", actorType: "human", occurredAt: when.Add(time.Second)},
		{id: uuid.NewString(), seq: 12, content: "cccccccc", actorType: "human", occurredAt: when.Add(2 * time.Second)},
		{id: uuid.NewString(), seq: 13, content: "dddddddd", actorType: "human", occurredAt: when.Add(3 * time.Second)},
	}
	messages, truncated := packGroupRecallMessages(anchor, neighbors, 7, maxGroupRecallMessages)
	if truncated || len(messages) != 4 || !messages[0].Anchor {
		t.Fatalf("one-sided packing messages=%#v truncated=%t", messages, truncated)
	}
}

func TestPackGroupRecallMessagesEnforcesMessageCap(t *testing.T) {
	when := time.Now().UTC()
	anchor := groupRecallMessage{id: uuid.NewString(), seq: 300, content: "anchor", actorType: "human", occurredAt: when.Add(300 * time.Second)}
	neighbors := make([]groupRecallMessage, 0, 205)
	for seq := int64(1); seq <= 205; seq++ {
		neighbors = append(neighbors, groupRecallMessage{
			id: uuid.NewString(), seq: seq, content: "a", actorType: "human", occurredAt: when.Add(time.Duration(seq) * time.Second),
		})
	}
	messages, truncated := packGroupRecallMessages(anchor, neighbors, 10_000, maxGroupRecallMessages)
	if !truncated || len(messages) != maxGroupRecallMessages || !messages[len(messages)-1].Anchor {
		t.Fatalf("message-cap packing count=%d truncated=%t", len(messages), truncated)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i].OccurredAt.Before(messages[i-1].OccurredAt) {
			t.Fatalf("message-cap output is not chronological at %d", i)
		}
	}
}

func seedGroupRecallMessage(t *testing.T, q *sqlc.Queries, groupID string, seq int64, actorType, actorID, displayName, content string) string {
	t.Helper()
	id := uuid.NewString()
	when := time.Date(2026, 8, 18, 0, 0, int(seq), 0, time.UTC)
	if _, err := q.CreateGroupMessage(t.Context(), sqlc.CreateGroupMessageParams{
		ID: id, GroupID: groupID, Seq: seq, ActorType: actorType, ActorID: actorID,
		ActorDisplayName:  pgtype.Text{String: displayName, Valid: displayName != ""},
		PlatformTimestamp: pgtype.Timestamptz{Time: when, Valid: true}, Content: content,
		ContentBlocks: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("seed group message seq=%d: %v", seq, err)
	}
	return id
}
