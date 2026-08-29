package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/sessionmedia"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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

// legacyImageBlocks is what a row written before canonical media replays as:
// provider bytes inline, the one shape the durable codec refuses. It is a
// literal because no writer produces this kind any more.
func legacyImageBlocks() []byte {
	return []byte(`[{"kind":"text","text":"look at this"},{"kind":"image","data":"aGk=","mime_type":"image/png"}]`)
}

// seedGroupMessageBlocks writes a message whose stored blocks are raw JSON, so
// a test can plant a shape no current writer produces.
func seedGroupMessageBlocks(t *testing.T, fx dispatcherFixture, id string, seq int64, blocks []byte) sqlc.CtxGroupMessage {
	t.Helper()
	ctx := context.Background()
	msg := createGroupMessage(t, fx.q, fx.groupID, id, seq, eventlog.ActorHuman, "user-1", ai.UnavailableImageProjection)
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_message SET content_blocks = $2 WHERE id = $1`, msg.ID, blocks); err != nil {
		t.Fatalf("seed content blocks: %v", err)
	}
	row, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// A group row written before canonical media still replays as raw bytes, which
// no durable commit accepts: the whole turn would roll back at the end. The
// first read migrates it instead, so the turn commits and the row is left in
// the canonical shape for everyone after it.
func TestGroupWakeMigratesLegacyInlineImageRow(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"text":"hi"}`)
	ctx := context.Background()
	images := &fakeGroupImages{baseline: testBaseline}
	fx.d.chats.coord.sessionImages = images

	msg := seedGroupMessageBlocks(t, fx, "c1c1c1c1-0000-0000-0000-000000000003", 2, legacyImageBlocks())

	blocks := fx.d.chats.triggerContent(ctx, fx.groupID, "agent-1", msg)

	// This is the exact gate the durable codec applies before writing rows.
	if err := ai.ValidateCanonicalContentBlocks(blocks); err != nil {
		t.Fatalf("migrated trigger blocks = %#v, still uncommittable: %v", blocks, err)
	}
	ref, ok := blocks[len(blocks)-1].(ai.ImageRefContent)
	if !ok || ref.Baseline.Text != testBaseline {
		t.Fatalf("turn blocks = %#v, want a described reference", blocks)
	}
	if images.persistOwner != sessionmedia.GroupOwner(uuid.MustParse(fx.groupID)) {
		t.Fatalf("migration owner = %#v, want the group", images.persistOwner)
	}

	row, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.ContentBlocks), `"image"`) || !strings.Contains(string(row.ContentBlocks), `"image_ref"`) {
		t.Fatalf("stored blocks = %s, want the legacy image rewritten as a reference", row.ContentBlocks)
	}
	if !strings.Contains(row.Content, "a street sign") {
		t.Fatalf("stored text projection = %q, want the rendered description", row.Content)
	}
}

// Migration is still degradation, never a lost turn: bytes that cannot be
// stored become the unavailable marker, which a commit can hold.
func TestGroupWakeDegradesLegacyRowWhenPersistFails(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"text":"hi"}`)
	fx.d.chats.coord.sessionImages = &fakeGroupImages{persistErr: errors.New("blob store down")}

	msg := seedGroupMessageBlocks(t, fx, "c1c1c1c1-0000-0000-0000-000000000004", 2, legacyImageBlocks())

	blocks := fx.d.chats.triggerContent(context.Background(), fx.groupID, "agent-1", msg)

	if err := ai.ValidateCanonicalContentBlocks(blocks); err != nil {
		t.Fatalf("degraded blocks = %#v, still uncommittable: %v", blocks, err)
	}
	if ai.HasImage(blocks) || ai.HasImageRef(blocks) {
		t.Fatalf("degraded blocks = %#v, want text only", blocks)
	}
}

// perAgentImages describes an image differently for each reader, so a lost
// update is visible in the stored row rather than hidden behind equal text.
type perAgentImages struct {
	mu      sync.Mutex
	renders int
}

func (p *perAgentImages) Persist(_ context.Context, _ sessionmedia.Owner, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return blocks, nil
}

func (p *perAgentImages) RenderBaselines(_ context.Context, _ sessionmedia.Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	p.mu.Lock()
	p.renders++
	p.mu.Unlock()
	out := ai.CloneContentBlocks(blocks)
	for i, block := range out {
		ref, ok := block.(ai.ImageRefContent)
		if !ok || ref.Baseline.Text != "" {
			continue
		}
		ref.Baseline = ai.ImageBaseline{Text: "described by " + agentID}
		out[i] = ref
	}
	return out, nil
}

// Two agents can wake on the same message and render it at the same time. The
// write-back is compare-and-set on the blocks the reader saw, so the second
// writer drops its result instead of clobbering the first: whoever loses simply
// re-renders on a later wake.
func TestGroupBaselineWriteBackIsCompareAndSet(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{"text":"hi"}`)
	ctx := context.Background()
	images := &perAgentImages{}
	fx.d.chats.coord.sessionImages = images

	stored := marshalGroupContentBlocks([]ai.ContentBlock{ai.ImageRefContent{MediaID: uuid.NewString()}})
	msg := seedGroupMessageBlocks(t, fx, "c1c1c1c1-0000-0000-0000-000000000005", 2, stored)

	var wg sync.WaitGroup
	for _, agentID := range []string{"agent-1", "agent-2"} {
		wg.Go(func() {
			fx.d.chats.baselinedContentBlocks(ctx, fx.groupID, agentID, msg)
		})
	}
	wg.Wait()

	if images.renders != 2 {
		t.Fatalf("renders = %d, want one per reader", images.renders)
	}
	row, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	winners := 0
	for _, agentID := range []string{"agent-1", "agent-2"} {
		if strings.Contains(string(row.ContentBlocks), "described by "+agentID) {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("stored blocks = %s, want exactly one writer's baseline", row.ContentBlocks)
	}

	// A reader holding the pre-render row can no longer overwrite what landed,
	// which is the lost update the CAS exists to stop.
	fx.d.chats.baselinedContentBlocks(ctx, fx.groupID, "agent-3", msg)
	after, err := fx.q.GetGroupMessage(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.ContentBlocks) != string(row.ContentBlocks) || after.Content != row.Content {
		t.Fatalf("stale reader overwrote the stored projection: %s / %q", after.ContentBlocks, after.Content)
	}
}
