package lcm

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type countingPartsQuerier struct {
	calls int
	ids   []string
	parts []sqlc.CtxMessagePart
}

func (q *countingPartsQuerier) GetMessagePartsByMessages(_ context.Context, ids []string) ([]sqlc.CtxMessagePart, error) {
	q.calls++
	q.ids = append([]string(nil), ids...)
	return q.parts, nil
}

func TestMessageIDsThatCanHavePartsSkipsPlainParents(t *testing.T) {
	msgs := []sqlc.CtxMessage{
		{ID: "plain-user", Role: roleUser, EventType: eventTypeText},
		{ID: "image-user", Role: roleUser, EventType: eventTypeMultimodal},
		{ID: "assistant", Role: roleAssistant, EventType: eventTypeText},
		{ID: "tool", Role: roleTool, EventType: eventTypeToolResult},
	}
	if got, want := messageIDsThatCanHaveParts(msgs), []string{"image-user", "tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("part candidate IDs = %v, want %v", got, want)
	}
}

func TestLoadMessagePartsUsesOneBatchQuery(t *testing.T) {
	q := &countingPartsQuerier{parts: []sqlc.CtxMessagePart{
		{ID: "p1", MessageID: "m1", PartType: "text", Ordinal: 0, TextContent: pgtype.Text{String: "first", Valid: true}},
		{ID: "p2", MessageID: "m2", PartType: "image", Ordinal: 0, MediaID: pgtype.Text{String: "media-2", Valid: true}, TextContent: pgtype.Text{String: ai.UnavailableImageProjection, Valid: true}},
	}}

	got, err := loadMessageParts(context.Background(), q, []string{"m1", "m2"})
	if err != nil {
		t.Fatalf("loadMessageParts: %v", err)
	}
	if q.calls != 1 {
		t.Fatalf("part query calls = %d, want 1", q.calls)
	}
	if want := []string{"m1", "m2"}; !reflect.DeepEqual(q.ids, want) {
		t.Fatalf("batch ids = %v, want %v", q.ids, want)
	}
	if len(got["m1"]) != 1 || len(got["m2"]) != 1 || got["m2"][0].MediaID.String != "media-2" {
		t.Fatalf("loaded parts = %#v", got)
	}
}
