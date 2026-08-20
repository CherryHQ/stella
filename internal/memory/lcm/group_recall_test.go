package lcm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func appendRecallGroupMessage(t *testing.T, store *eventlog.Store, groupID, content string) memory.GroupRecallResult {
	t.Helper()
	result, err := store.AppendToGroup(context.Background(), groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman, ActorID: "person", Content: content,
	})
	if err != nil {
		t.Fatalf("append group message: %v", err)
	}
	return memory.GroupRecallResult{ID: result.Message.ID, Seq: result.Message.Seq, Content: content}
}

func newGroupRecallProvider(t *testing.T) (*Provider, *pgxpool.Pool, *sqlc.Queries, *eventlog.Store, string) {
	t.Helper()
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform: "test", PlatformGroupID: "recall-" + t.Name(), PlatformMessageID: "first",
		ActorType: eventlog.ActorHuman, ActorID: "person", Content: "first",
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, db, sqlc.New(db), store, first.GroupID
}

func TestGroupRecallSearchUsesCanonicalDeliveredTextAndDisplaySnapshot(t *testing.T) {
	p, db, q, store, groupID := newGroupRecallProvider(t)
	nameMatch := appendRecallGroupMessage(t, store, groupID, "status update")
	if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: nameMatch.ID, DeliveryState: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE ctx_group_message SET actor_display_name = '李雷' WHERE id = $1`, nameMatch.ID); err != nil {
		t.Fatal(err)
	}
	chinese := appendRecallGroupMessage(t, store, groupID, "今天确认部署方案")
	pending := appendRecallGroupMessage(t, store, groupID, "deployment pending secret")
	if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: pending.ID, DeliveryState: "pending"}); err != nil {
		t.Fatal(err)
	}
	failed := appendRecallGroupMessage(t, store, groupID, "deployment failed secret")
	if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: failed.ID, DeliveryState: "failed"}); err != nil {
		t.Fatal(err)
	}
	trigger := appendRecallGroupMessage(t, store, groupID, "trigger")
	future := appendRecallGroupMessage(t, store, groupID, "deployment future secret")

	byName, err := p.SearchGroupRecall(context.Background(), groupID, trigger.Seq, "李雷", 20)
	if err != nil || len(byName) != 1 || byName[0].ID != nameMatch.ID || byName[0].Score <= 0 {
		t.Fatalf("display-name recall = %+v, err=%v", byName, err)
	}
	byContent, err := p.SearchGroupRecall(context.Background(), groupID, trigger.Seq, "部署方案", 20)
	if err != nil || len(byContent) != 1 || byContent[0].ID != chinese.ID || byContent[0].Score <= 0 {
		t.Fatalf("Chinese recall = %+v, err=%v", byContent, err)
	}
	english, err := p.SearchGroupRecall(context.Background(), groupID, trigger.Seq, "status", 20)
	if err != nil || len(english) != 1 || english[0].ID != nameMatch.ID {
		t.Fatalf("English recall = %+v, err=%v", english, err)
	}
	for _, query := range []string{"***", " \t\n "} {
		rows, err := p.SearchGroupRecall(context.Background(), groupID, trigger.Seq, query, 20)
		if err != nil || len(rows) != 0 {
			t.Fatalf("punctuation query %q = %+v, err=%v", query, rows, err)
		}
	}
	for _, id := range []string{pending.ID, failed.ID, future.ID} {
		if _, _, err := p.ReadGroupRecall(context.Background(), groupID, trigger.Seq, id, 100); !errors.Is(err, memory.ErrGroupRecallNotFound) {
			t.Fatalf("non-public or future ref %q error=%v, want not found", id, err)
		}
	}
}

func TestGroupRecallExplainUsesBM25AndCanonicalScope(t *testing.T) {
	_, db, _, store, groupID := newGroupRecallProvider(t)
	for i := range 100 {
		appendRecallGroupMessage(t, store, groupID, fmt.Sprintf("deployment evidence %03d", i))
	}
	rows, err := db.Query(context.Background(), `
		EXPLAIN (COSTS OFF)
		SELECT id
		FROM ctx_group_message
		WHERE id @@@ paradedb.match('content', 'deployment')
		  AND group_id = $1
		  AND seq < 10_000
		  AND delivery_state = 'delivered'
		  AND btrim(content) <> ''
		ORDER BY paradedb.score(id) DESC, created_at DESC, id DESC
		LIMIT 20`, groupID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "idx_ctx_group_message_bm25") || !strings.Contains(joined, "group_id") || !strings.Contains(joined, "delivery_state") {
		t.Fatalf("group recall plan did not use BM25 plus canonical filters:\n%s", joined)
	}
}

func TestGroupRecallReadTransfersUnusedSideBudgetAndCapsMessages(t *testing.T) {
	p, _, _, store, groupID := newGroupRecallProvider(t)
	appendRecallGroupMessage(t, store, groupID, "left")
	anchor := appendRecallGroupMessage(t, store, groupID, "anchor")
	rightOne := appendRecallGroupMessage(t, store, groupID, strings.Repeat("r", 40))
	rightTwo := appendRecallGroupMessage(t, store, groupID, strings.Repeat("s", 40))
	trigger := appendRecallGroupMessage(t, store, groupID, "trigger")
	rows, truncated, err := p.ReadGroupRecall(context.Background(), groupID, trigger.Seq, anchor.ID, 25)
	if err != nil || truncated || len(rows) != 5 || rows[len(rows)-1].ID != rightTwo.ID || rows[len(rows)-2].ID != rightOne.ID {
		t.Fatalf("unused budget did not transfer to the right: rows=%+v truncated=%t err=%v", rows, truncated, err)
	}

	p, _, _, store, groupID = newGroupRecallProvider(t)
	var anchors []memory.GroupRecallResult
	for range 205 {
		anchors = append(anchors, appendRecallGroupMessage(t, store, groupID, "short"))
	}
	trigger = appendRecallGroupMessage(t, store, groupID, "trigger")
	rows, truncated, err = p.ReadGroupRecall(context.Background(), groupID, trigger.Seq, anchors[102].ID, 8_000)
	if err != nil || !truncated || len(rows) != maxGroupRecallMessages {
		t.Fatalf("message cap: count=%d truncated=%t err=%v", len(rows), truncated, err)
	}
}

func TestGroupRecallReadExpandsChronologicallyAndBounds(t *testing.T) {
	p, _, _, store, groupID := newGroupRecallProvider(t)
	for range 3 {
		appendRecallGroupMessage(t, store, groupID, strings.Repeat("l", 20))
	}
	anchor := appendRecallGroupMessage(t, store, groupID, strings.Repeat("a", 20))
	for range 3 {
		appendRecallGroupMessage(t, store, groupID, strings.Repeat("r", 20))
	}
	trigger := appendRecallGroupMessage(t, store, groupID, "trigger")
	rows, truncated, err := p.ReadGroupRecall(context.Background(), groupID, trigger.Seq, anchor.ID, 200)
	if err != nil || truncated || len(rows) != 8 {
		t.Fatalf("read = %+v truncated=%t err=%v", rows, truncated, err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Seq >= rows[i].Seq {
			t.Fatalf("read is not chronological: %+v", rows)
		}
	}

	big := appendRecallGroupMessage(t, store, groupID, strings.Repeat("x", 1_000))
	rows, truncated, err = p.ReadGroupRecall(context.Background(), groupID, big.Seq+1, big.ID, 10)
	if err != nil || !truncated || len(rows) != 1 || len(rows[0].Content) > 40 {
		t.Fatalf("oversized anchor = %+v truncated=%t err=%v", rows, truncated, err)
	}
}
