package lcm

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
)

// The private half of group assembly must be bounded like the public window.
// The bound is the caller's budget, so it can only drop turns that
// trimOldestCompleteTurns was going to drop anyway.
func TestGroupPrivateReadIsBoundedAtTurnGranularity(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendWindowMessage(t, store, "private-read", eventlog.ActorHuman, "u1", "Ann", "start")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	session := groupSess("agent-a", first.GroupID)
	ctx := groupCtx(first.Seq)

	// Twelve turns of roughly 100 tokens each, oldest first.
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
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		t.Fatal(err)
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

	// A small budget keeps only the newest turns, and every retained turn still
	// opens on its own user anchor rather than a headless continuation.
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

	// The bound must not change what assembly produces for a budget that fits.
	messages, err := p.Assemble(ctx, session, 1_000_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	for _, want := range []string{"anchor-a", "anchor-l"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("assembly under a generous budget lost %q", want)
		}
	}
}
