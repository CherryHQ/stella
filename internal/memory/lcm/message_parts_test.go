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
	partCalls  int
	mediaCalls int
	partIDs    []string
	mediaIDs   []string
	parts      []sqlc.CtxMessagePart
	media      []sqlc.ListMessagePartsWithMediaByMessagesRow
}

func (q *countingPartsQuerier) GetMessagePartsByMessages(_ context.Context, ids []string) ([]sqlc.CtxMessagePart, error) {
	q.partCalls++
	q.partIDs = append([]string(nil), ids...)
	return q.parts, nil
}

func (q *countingPartsQuerier) ListMessagePartsWithMediaByMessages(_ context.Context, ids []string) ([]sqlc.ListMessagePartsWithMediaByMessagesRow, error) {
	q.mediaCalls++
	q.mediaIDs = append([]string(nil), ids...)
	return q.media, nil
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

func TestLoadMessagePartsUsesOneBatchQueryPerPartAndMedia(t *testing.T) {
	q := &countingPartsQuerier{
		parts: []sqlc.CtxMessagePart{
			{ID: "p1", MessageID: "m1", PartType: "text", Ordinal: 0, TextContent: pgtype.Text{String: "first", Valid: true}},
			{ID: "p2", MessageID: "m2", PartType: "image", Ordinal: 0, MediaID: pgtype.Text{String: "media-2", Valid: true}, TextContent: pgtype.Text{String: ai.UnavailableImageProjection, Valid: true}},
		},
		media: []sqlc.ListMessagePartsWithMediaByMessagesRow{{
			CtxMessagePart: sqlc.CtxMessagePart{ID: "p2"},
			CtxMedium:      sqlc.CtxMedium{ID: "media-2", MimeType: "image/png"},
		}},
	}

	got, err := loadMessageParts(context.Background(), q, []string{"m1", "m2"})
	if err != nil {
		t.Fatalf("loadMessageParts: %v", err)
	}
	if q.partCalls != 1 || q.mediaCalls != 1 {
		t.Fatalf("part/media query calls = %d/%d, want 1/1", q.partCalls, q.mediaCalls)
	}
	if want := []string{"m1", "m2"}; !reflect.DeepEqual(q.partIDs, want) || !reflect.DeepEqual(q.mediaIDs, want) {
		t.Fatalf("batch ids = parts:%v media:%v, want %v", q.partIDs, q.mediaIDs, want)
	}
	if len(got["m1"]) != 1 || len(got["m2"]) != 1 {
		t.Fatalf("loaded parts = %#v", got)
	}
	if got["m2"][0].media == nil || got["m2"][0].media.MimeType != "image/png" {
		t.Fatalf("media metadata missing: %#v", got["m2"])
	}
}
