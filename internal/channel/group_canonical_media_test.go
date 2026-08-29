package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/sessionmedia"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// fakeGroupImages records the owner it was handed and swaps raw bytes for
// references, standing in for the real pipeline's blob and VLM work.
type fakeGroupImages struct {
	persistOwner  sessionmedia.Owner
	renderOwner   sessionmedia.Owner
	renderAgentID string
	renders       int
	baseline      string
	persistErr    error
	renderErr     error
}

func (f *fakeGroupImages) Persist(_ context.Context, owner sessionmedia.Owner, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	f.persistOwner = owner
	if f.persistErr != nil {
		return nil, f.persistErr
	}
	out := ai.CloneContentBlocks(blocks)
	for i, block := range out {
		if _, ok := block.(ai.ImageContent); ok {
			out[i] = ai.ImageRefContent{MediaID: uuid.NewString()}
		}
	}
	return out, nil
}

func (f *fakeGroupImages) RenderBaselines(_ context.Context, owner sessionmedia.Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	f.renderOwner, f.renderAgentID = owner, agentID
	f.renders++
	if f.renderErr != nil {
		return nil, f.renderErr
	}
	out := ai.CloneContentBlocks(blocks)
	for i, block := range out {
		ref, ok := block.(ai.ImageRefContent)
		if !ok || ref.Baseline.Text != "" {
			continue
		}
		ref.Baseline = ai.ImageBaseline{Text: f.baseline}
		out[i] = ref
	}
	return out, nil
}

const testBaseline = "## Text\nsign\n\n## Scene\na street sign"

func groupImageMessage() pkgchannel.IncomingMessage {
	return pkgchannel.IncomingMessage{
		Platform: "telegram", ChatID: "group-42", IsGroup: true,
		SenderID: "user-1", MessageID: "m-1",
		Content: []ai.ContentBlock{
			ai.TextContent{Text: "look at this"},
			ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
		},
	}
}

// Ingestion stores a group image through the shared media path and owns it by
// group, not by the sender: the reference it writes carries no baseline,
// because describing an image nobody reads is a VLM call spent for nothing.
func TestGroupIngestPersistsGroupOwnedReferencesWithoutBaseline(t *testing.T) {
	db := dbtest.New(t)
	store := eventlog.NewStore(db)
	images := &fakeGroupImages{baseline: testBaseline}
	c := &Coordinator{eventLog: store, sessionImages: images}
	ctx := context.Background()

	content := c.canonicalGroupContent(ctx, groupImageMessage())

	if len(content) != 2 {
		t.Fatalf("content = %#v, want text + reference", content)
	}
	ref, ok := content[1].(ai.ImageRefContent)
	if !ok {
		t.Fatalf("content[1] = %#v, want a canonical reference", content[1])
	}
	if ref.Baseline.Text != "" {
		t.Fatalf("ingestion rendered a baseline eagerly: %q", ref.Baseline.Text)
	}
	groupID, err := store.ResolveGroupID(ctx, "telegram", "group-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if images.persistOwner != sessionmedia.GroupOwner(uuid.MustParse(groupID)) {
		t.Fatalf("persist owner = %#v, want the group %s", images.persistOwner, groupID)
	}
	// Triage routes on the text column, so an image-only projection must still
	// say something; the stored blocks keep the reference itself.
	if got := ai.FlattenCanonicalText(content); got != "look at this "+ai.UnavailableImageProjection {
		t.Fatalf("projection = %q", got)
	}
	if blocks := marshalGroupContentBlocks(content); !strings.Contains(string(blocks), `"image_ref"`) {
		t.Fatalf("stored blocks = %s, want a canonical reference", blocks)
	}
}

// A media failure costs the image, never the message: the text still lands and
// the image becomes the stable unavailable marker.
func TestGroupIngestDegradesWhenMediaFails(t *testing.T) {
	db := dbtest.New(t)
	c := &Coordinator{
		eventLog:      eventlog.NewStore(db),
		sessionImages: &fakeGroupImages{persistErr: errors.New("blob store down")},
	}

	content := c.canonicalGroupContent(context.Background(), groupImageMessage())

	if ai.HasImage(content) || ai.HasImageRef(content) {
		t.Fatalf("degraded content = %#v, want text only", content)
	}
	got := ai.FlattenCanonicalText(content)
	if !strings.Contains(got, "look at this") || !strings.Contains(got, ai.UnavailableImageProjection) {
		t.Fatalf("degraded projection = %q", got)
	}
}

// The first turn that reads a group image pays for its description and writes
// it back, so every later reader gets it for free. Both projections of the same
// blocks are rewritten together: the text column feeds the history window that
// other agents read, and it may not disagree with content_blocks.
func TestGroupWakeRendersBaselineOnceAndWritesItBack(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"text":"hi"}`)
	ctx := context.Background()
	images := &fakeGroupImages{baseline: testBaseline}
	fx.d.chats.coord.sessionImages = images

	mediaID := uuid.NewString()
	stored := marshalGroupContentBlocks([]ai.ContentBlock{
		ai.TextContent{Text: "look at this"},
		ai.ImageRefContent{MediaID: mediaID},
	})
	msg := createGroupMessage(t, fx.q, fx.groupID, "c1c1c1c1-0000-0000-0000-000000000001", 2, eventlog.ActorHuman, "user-1", ai.UnavailableImageProjection)
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_message SET content_blocks = $2 WHERE id = $1`, msg.ID, stored); err != nil {
		t.Fatalf("seed content blocks: %v", err)
	}
	msg, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}

	blocks := fx.d.chats.triggerContent(ctx, fx.groupID, "agent-1", msg)

	if images.renderOwner != sessionmedia.GroupOwner(uuid.MustParse(fx.groupID)) {
		t.Fatalf("render owner = %#v, want the group", images.renderOwner)
	}
	if images.renderAgentID != "agent-1" {
		t.Fatalf("render agent = %q, want the woken agent", images.renderAgentID)
	}
	ref, ok := blocks[len(blocks)-1].(ai.ImageRefContent)
	if !ok || ref.Baseline.Text != testBaseline {
		t.Fatalf("turn blocks = %#v, want a described reference", blocks)
	}

	row, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.Content, "a street sign") {
		t.Fatalf("stored text projection = %q, want the rendered description", row.Content)
	}
	if !strings.Contains(string(row.ContentBlocks), "a street sign") {
		t.Fatalf("stored blocks = %s, want the rendered baseline", row.ContentBlocks)
	}

	// A described image is never re-rendered: the second reader of the same row
	// spends no VLM call.
	if got := fx.d.chats.triggerContent(ctx, fx.groupID, "agent-1", row); !ai.HasImageRef(got) {
		t.Fatalf("second read = %#v, want the reference", got)
	}
	if images.renders != 1 {
		t.Fatalf("renders = %d, want exactly one", images.renders)
	}
}

// Rendering is best effort: a VLM failure leaves the bare reference in place,
// rewrites nothing, and lets the next turn try again.
func TestGroupWakeKeepsBareReferenceWhenRenderFails(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"text":"hi"}`)
	ctx := context.Background()
	fx.d.chats.coord.sessionImages = &fakeGroupImages{renderErr: errors.New("vision down")}

	stored := marshalGroupContentBlocks([]ai.ContentBlock{ai.ImageRefContent{MediaID: uuid.NewString()}})
	msg := createGroupMessage(t, fx.q, fx.groupID, "c1c1c1c1-0000-0000-0000-000000000002", 2, eventlog.ActorHuman, "user-1", ai.UnavailableImageProjection)
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_message SET content_blocks = $2 WHERE id = $1`, msg.ID, stored); err != nil {
		t.Fatalf("seed content blocks: %v", err)
	}
	msg, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}

	blocks := fx.d.chats.triggerContent(ctx, fx.groupID, "agent-1", msg)
	ref, ok := blocks[len(blocks)-1].(ai.ImageRefContent)
	if !ok || ref.Baseline.Text != "" {
		t.Fatalf("turn blocks = %#v, want the bare reference", blocks)
	}
	row, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Content != ai.UnavailableImageProjection {
		t.Fatalf("failed render rewrote the row: %q", row.Content)
	}
}
