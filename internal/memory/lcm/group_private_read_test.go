package lcm

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func groupReadTestSetup(t *testing.T, name string) (*Provider, string, string) {
	t.Helper()
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendWindowMessage(t, store, name, eventlog.ActorHuman, "u1", "Ann", "start")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	session := groupSess("agent-a", first.GroupID)
	convID, err := p.getOrCreateConversation(groupCtx(first.Seq), session)
	if err != nil {
		t.Fatal(err)
	}
	return p, first.GroupID, convID
}

// The private half of group assembly must be bounded like the public window,
// and the bound is the caller's budget so it can only drop turns that
// trimOldestCompleteTurns was going to drop anyway.
func TestGroupPrivateReadIsBoundedAtTurnGranularity(t *testing.T) {
	p, groupID, convID := groupReadTestSetup(t, "private-read")
	session := groupSess("agent-a", groupID)
	ctx := groupCtx(1)

	body := strings.Repeat("word ", 100)
	for i := range 12 {
		turn := []ai.Message{
			ai.UserMessage{Content: "anchor-" + string(rune('a'+i))},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: body}}},
		}
		if err := p.Append(ctx, session, turn...); err != nil {
			t.Fatalf("append turn %d: %v", i, err)
		}
	}

	all, err := p.loadGroupPrivateRows(ctx, convID, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 24 {
		t.Fatalf("generous budget read %d rows, want all 24", len(all))
	}
	if all[0].Role != roleUser || !strings.Contains(all[0].Content, "anchor-a") {
		t.Fatalf("untruncated read did not start at the oldest anchor: %+v", all[0])
	}

	bounded, err := p.loadGroupPrivateRows(ctx, convID, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) == 0 || len(bounded) >= len(all) {
		t.Fatalf("bounded read = %d rows, want a nonempty subset of %d", len(bounded), len(all))
	}
	if bounded[0].Role != roleUser {
		t.Fatalf("bounded read opened on a headless row: %+v", bounded[0])
	}
	if last := bounded[len(bounded)-1]; last.Seq != all[len(all)-1].Seq {
		t.Fatalf("bounded read dropped the newest rows: last seq %d, want %d", last.Seq, all[len(all)-1].Seq)
	}
}

// The read must cost a row exactly as the trim costs the message it becomes.
// token_count is written from the row's token text and charges a thinking
// block's full body and a legacy tool envelope's inline base64; the trim
// charges neither, so a stored-count bound would cut turns the trim keeps.
func TestGroupPrivateReadCostsRowsLikeTheTrim(t *testing.T) {
	p, groupID, convID := groupReadTestSetup(t, "private-cost")
	session := groupSess("agent-a", groupID)
	ctx := groupCtx(1)

	thinking := strings.Repeat("reason ", 400)
	image := strings.Repeat("A", 40_000)
	turns := []ai.Message{
		ai.UserMessage{Content: "q"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ThinkingContent{Thinking: thinking}}},
		ai.ToolResultMessage{ToolCallID: "c", ToolName: "shot", Content: []ai.ContentBlock{ai.ImageContent{MimeType: "image/png", Data: image}}},
	}
	if err := p.Append(ctx, session, turns...); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(p.db)
	rows, err := q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	stored := 0
	for _, row := range rows {
		stored += int(row.TokenCount)
	}
	if stored < 10_000 {
		t.Fatalf("fixture did not produce a large stored count: %d", stored)
	}

	// A budget far below the stored sum, but at or above what the trim charges,
	// must still read the whole turn.
	budget := 64
	bounded, err := p.loadGroupPrivateRows(ctx, convID, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) != len(rows) {
		t.Fatalf("stored-count skew cut the turn: read %d of %d rows at budget %d (stored %d)", len(bounded), len(rows), budget, stored)
	}
}

// Reverse paging starts with no lower bound at all rather than a sentinel,
// because bigint has no value above every legal seq.
func TestGroupPrivateReadCoversMaxSeqRow(t *testing.T) {
	p, groupID, convID := groupReadTestSetup(t, "private-maxseq")
	session := groupSess("agent-a", groupID)
	ctx := groupCtx(1)
	if err := p.Append(ctx, session, ai.UserMessage{Content: "newest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(ctx, `UPDATE ctx_message SET seq = 9223372036854775807 WHERE conversation_id = $1`, convID); err != nil {
		t.Fatal(err)
	}
	rows, err := p.loadGroupPrivateRows(ctx, convID, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Content != "newest" {
		t.Fatalf("max-seq row was skipped by the page sentinel: %+v", rows)
	}
}

// A page-aligned exact fill exits the outer loop without the inner loop ever
// probing the next row. The cut is real, so it must still be reported as
// truncated or the headless remainder survives as an anchorless private turn.
func TestGroupPrivateReadReportsExactFillAsTruncated(t *testing.T) {
	p, groupID, convID := groupReadTestSetup(t, "private-exactfill")
	session := groupSess("agent-a", groupID)
	ctx := groupCtx(1)
	for range 3 {
		turn := []ai.Message{
			ai.UserMessage{Content: "anchor"},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "body"}}},
		}
		if err := p.Append(ctx, session, turn...); err != nil {
			t.Fatal(err)
		}
	}
	// Four rows is an exact fill that cuts the oldest turn in half: rows 3..6 are
	// assistant, user, assistant, user-anchored continuation in reverse order.
	rows, err := p.loadGroupPrivateRowsCapped(ctx, convID, 1_000_000, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("exact fill returned nothing")
	}
	if rows[0].Role != roleUser {
		t.Fatalf("exact fill left a headless remainder: %+v", rows[0])
	}
}
