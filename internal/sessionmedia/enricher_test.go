package sessionmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// fakePersister is a content-addressed in-memory ctx_media: one row per
// (owner, sha256), carrying at most one baseline. That identity is what the
// production store guarantees, and without it a test cannot tell "described
// once per media object" apart from "described once per message".
type fakePersister struct {
	mu        sync.Mutex
	inputs    []Input
	objects   map[string]storedObject
	ids       map[string]string
	baselines map[string]ai.ImageBaseline
	loads     atomic.Int64
	storeErr  error
	amnesiac  bool
	started   chan struct{}
	release   <-chan struct{}
	active    atomic.Int64
	max       atomic.Int64
}

type storedObject struct {
	owner Owner
	data  []byte
	mime  string
}

func mediaKey(owner Owner, data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%s/%s/%x", owner.Kind, owner.ID, digest)
}

func (p *fakePersister) Persist(ctx context.Context, in Input) (string, ai.ImageBaseline, error) {
	active := p.active.Add(1)
	for {
		max := p.max.Load()
		if active <= max || p.max.CompareAndSwap(max, active) {
			break
		}
	}
	defer p.active.Add(-1)
	if p.started != nil {
		p.started <- struct{}{}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return "", ai.ImageBaseline{}, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.inputs = append(p.inputs, in)
	key := mediaKey(in.Owner, in.Data)
	mediaID, ok := p.ids[key]
	if !ok {
		mediaID = fmt.Sprintf("media-%d", len(p.ids)+1)
		p.ids[key] = mediaID
		p.objects[mediaID] = storedObject{owner: in.Owner, data: in.Data, mime: in.MimeType}
	}
	return mediaID, p.baselines[mediaID], nil
}

// Load reopens what Persist stored, so a lazy render sees exactly the bytes
// ingestion kept.
func (p *fakePersister) Load(_ context.Context, owner Owner, mediaID string) (ai.ImageContent, error) {
	p.loads.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	object, ok := p.objects[mediaID]
	if !ok || object.owner != owner {
		return ai.ImageContent{}, ErrNotFound
	}
	return ai.ImageContent{Data: base64.StdEncoding.EncodeToString(object.data), MimeType: object.mime}, nil
}

func (p *fakePersister) Baselines(_ context.Context, owner Owner, mediaIDs []string) (map[string]ai.ImageBaseline, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]ai.ImageBaseline)
	for _, mediaID := range mediaIDs {
		if object, ok := p.objects[mediaID]; !ok || object.owner != owner {
			continue
		}
		if baseline, ok := p.baselines[mediaID]; ok {
			out[mediaID] = baseline
		}
	}
	return out, nil
}

// StoreBaseline is first-write-wins, exactly like the UPDATE ... WHERE baseline
// IS NULL it stands in for.
func (p *fakePersister) StoreBaseline(_ context.Context, owner Owner, mediaID string, baseline ai.ImageBaseline) (ai.ImageBaseline, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if p.storeErr != nil {
		return ai.ImageBaseline{}, p.storeErr
	}
	if p.amnesiac {
		return ai.ImageBaseline{}, nil
	}
	if object, ok := p.objects[mediaID]; !ok || object.owner != owner {
		return ai.ImageBaseline{}, nil
	}
	if existing, ok := p.baselines[mediaID]; ok {
		return existing, nil
	}
	p.baselines[mediaID] = baseline
	return baseline, nil
}

// seedBaseline pretends an earlier reader already described this object.
func (p *fakePersister) seedBaseline(owner Owner, mediaID string, data []byte, mime string, baseline ai.ImageBaseline) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.ids[mediaKey(owner, data)] = mediaID
	p.objects[mediaID] = storedObject{owner: owner, data: data, mime: mime}
	p.baselines[mediaID] = baseline
}

func (p *fakePersister) init() {
	if p.objects == nil {
		p.objects = make(map[string]storedObject)
		p.ids = make(map[string]string)
		p.baselines = make(map[string]ai.ImageBaseline)
	}
}

type fakeBaselineRenderer struct {
	baseline ai.ImageBaseline
	err      error
	started  chan struct{}
	release  <-chan struct{}
	calls    atomic.Int64
	active   atomic.Int64
	max      atomic.Int64
}

func (r *fakeBaselineRenderer) Baseline(ctx context.Context, _ vision.Request) (ai.ImageBaseline, error) {
	r.calls.Add(1)
	active := r.active.Add(1)
	for {
		max := r.max.Load()
		if active <= max || r.max.CompareAndSwap(max, active) {
			break
		}
	}
	defer r.active.Add(-1)
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ai.ImageBaseline{}, ctx.Err()
		}
	}
	return r.baseline, r.err
}

type uncooperativeRenderer struct {
	started chan struct{}
	release <-chan struct{}
	active  atomic.Int64
	max     atomic.Int64
}

func (r *uncooperativeRenderer) Baseline(context.Context, vision.Request) (ai.ImageBaseline, error) {
	active := r.active.Add(1)
	for {
		max := r.max.Load()
		if active <= max || r.max.CompareAndSwap(max, active) {
			break
		}
	}
	defer r.active.Add(-1)
	r.started <- struct{}{}
	<-r.release // deliberately ignores ctx; the test releases it after Enrich returns.
	return validBaseline(), nil
}

// testOwner is a fresh user principal; owner kind is irrelevant to enrichment
// mechanics, which is exactly what the owner-kind tests below pin down.
func testOwner() Owner { return UserOwner(uuid.New()) }

func newFakeEnricher(t testing.TB, renderer vision.BaselineRenderer, opts PipelineOptions) (*enricher, *fakePersister, *atomic.Int64) {
	t.Helper()
	media := &fakePersister{}
	var resolves atomic.Int64
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		resolves.Add(1)
		return renderer
	}), opts)
	if err != nil {
		t.Fatalf("newEnricher: %v", err)
	}
	return enricher, media, &resolves
}

func imageBlock(t testing.TB, n byte) ai.ImageContent {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: n, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return ai.ImageContent{Data: base64.StdEncoding.EncodeToString(buf.Bytes()), MimeType: "image/png"}
}

func validBaseline() ai.ImageBaseline {
	return ai.ImageBaseline{Text: "## Text\nhello\n\n## Scene\na small image"}
}

type fakeSnapshotLoader struct {
	calls atomic.Int64
	snap  *config.Snapshot
	err   error
}

func (l *fakeSnapshotLoader) Snapshot(context.Context, string) (*config.Snapshot, error) {
	l.calls.Add(1)
	return l.snap, l.err
}

func TestSnapshotVisionFactoryReloadsForEachMessage(t *testing.T) {
	loader := &fakeSnapshotLoader{snap: &config.Snapshot{}}
	var builds atomic.Int64
	factory, err := newSnapshotVisionFactory(loader, func(_, _, _ string) (providers.StreamFunc, error) {
		builds.Add(1)
		return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
			return nil, errors.New("not called by factory test")
		}, nil
	})
	if err != nil {
		t.Fatalf("newSnapshotVisionFactory: %v", err)
	}
	first := factory.ForMessage(context.Background(), "agent")
	if first.(*vision.Service).ModelConfigured() {
		t.Fatal("empty first snapshot unexpectedly configured a VLM")
	}
	loader.snap = &config.Snapshot{ModelVision: "openai/vlm", Provider: "openai", Providers: map[string]config.ProviderCreds{"openai": {Type: "openai-completions", APIKey: "key"}}}
	second := factory.ForMessage(context.Background(), "agent")
	if !second.(*vision.Service).ModelConfigured() || builds.Load() != 1 || loader.calls.Load() != 2 {
		t.Fatalf("reload result = configured:%t builds:%d loads:%d", second.(*vision.Service).ModelConfigured(), builds.Load(), loader.calls.Load())
	}
	loader.err = errors.New("settings unavailable")
	if fallback := factory.ForMessage(context.Background(), "agent"); fallback.(*vision.Service).ModelConfigured() {
		t.Fatal("snapshot failure did not fall back to local extraction")
	}
}

func TestEnricherCanonicalizesUserAndToolBlocks(t *testing.T) {
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, media, resolves := newFakeEnricher(t, renderer, PipelineOptions{})
	user := []ai.ContentBlock{ai.TextContent{Text: "user text"}, imageBlock(t, 1)}
	tool := []ai.ContentBlock{imageBlock(t, 2), ai.TextContent{Text: "tool text"}}

	userOut, err := enricher.Enrich(context.Background(), testOwner(), "agent", user)
	if err != nil {
		t.Fatalf("enrich user: %v", err)
	}
	toolOut, err := enricher.Enrich(context.Background(), testOwner(), "agent", tool)
	if err != nil {
		t.Fatalf("enrich tool: %v", err)
	}
	for _, blocks := range [][]ai.ContentBlock{userOut, toolOut} {
		for _, block := range blocks {
			if _, ok := block.(ai.ImageContent); ok {
				t.Fatalf("enriched blocks retained raw base64: %#v", block)
			}
		}
		if err := ai.ValidateCanonicalContentBlocks(blocks); err != nil {
			t.Fatalf("canonical blocks: %v", err)
		}
	}
	if userOut[0] != user[0] || toolOut[1] != tool[1] {
		t.Fatal("text block order changed")
	}
	if len(media.inputs) != 2 || resolves.Load() != 2 {
		t.Fatalf("persists/resolutions = %d/%d, want 2/2", len(media.inputs), resolves.Load())
	}
}

func TestEnricherPersistsAllMediaBeforeVisionResolution(t *testing.T) {
	media := &fakePersister{}
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	var resolved atomic.Bool
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		media.mu.Lock()
		persisted := len(media.inputs)
		media.mu.Unlock()
		if persisted != 3 {
			t.Errorf("factory ran after %d persists, want all 3", persisted)
		}
		resolved.Store(true)
		return renderer
	}), PipelineOptions{})
	if err != nil {
		t.Fatalf("newEnricher: %v", err)
	}
	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if !resolved.Load() || len(out) != 3 {
		t.Fatalf("factory resolution/output = %t/%d", resolved.Load(), len(out))
	}
}

func TestEnricherLimitsPersistenceConcurrencyToTwo(t *testing.T) {
	release := make(chan struct{})
	media := &fakePersister{started: make(chan struct{}, 3), release: release}
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer
	}), PipelineOptions{})
	if err != nil {
		t.Fatalf("newEnricher: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
		result <- err
	}()
	<-media.started
	<-media.started
	if got := media.max.Load(); got != MaxConcurrentEnrichments {
		t.Fatalf("concurrent persistence = %d, want %d", got, MaxConcurrentEnrichments)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("Enrich: %v", err)
	}
}

func TestEnricherNilFactoryRendererIsStableUnavailable(t *testing.T) {
	media := &fakePersister{}
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return nil
	}), PipelineOptions{})
	if err != nil {
		t.Fatalf("newEnricher: %v", err)
	}
	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	ref := out[0].(ai.ImageRefContent)
	if ref.MediaID == "" || ref.Baseline != (ai.ImageBaseline{}) {
		t.Fatalf("nil renderer ref = %+v, want persisted stable unavailable", ref)
	}
}

func TestEnricherDeduplicatesBaselineWithinMessage(t *testing.T) {
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, media, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	image := imageBlock(t, 1)
	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{image, ai.TextContent{Text: "between"}, image})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(media.inputs) != 1 || renderer.max.Load() != 1 {
		t.Fatalf("duplicate did duplicate work: persisted=%d active=%d", len(media.inputs), renderer.max.Load())
	}
	first := out[0].(ai.ImageRefContent)
	second := out[2].(ai.ImageRefContent)
	if first != second {
		t.Fatalf("duplicate refs differ: %+v / %+v", first, second)
	}
}

func TestEnricherRejectsMalformedRendererOutput(t *testing.T) {
	renderer := &fakeBaselineRenderer{baseline: ai.ImageBaseline{Text: "not the baseline contract"}}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got := out[0].(ai.ImageRefContent).Baseline; got != (ai.ImageBaseline{}) {
		t.Fatalf("malformed renderer baseline = %+v, want stable unavailable", got)
	}
}

func TestEnricherUnavailableIsStableAndDoesNotStoreBackendError(t *testing.T) {
	renderer := &fakeBaselineRenderer{err: errors.New("provider secret backend error")}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	input := []ai.ContentBlock{imageBlock(t, 1)}
	first, err := enricher.Enrich(context.Background(), testOwner(), "agent", input)
	if err != nil {
		t.Fatalf("first Enrich: %v", err)
	}
	second, err := enricher.Enrich(context.Background(), testOwner(), "agent", input)
	if err != nil {
		t.Fatalf("second Enrich: %v", err)
	}
	firstRef := first[0].(ai.ImageRefContent)
	secondRef := second[0].(ai.ImageRefContent)
	if firstRef.Baseline != (ai.ImageBaseline{}) || secondRef.Baseline != firstRef.Baseline {
		t.Fatalf("unavailable baseline is not byte-stable: %+v / %+v", firstRef.Baseline, secondRef.Baseline)
	}
	projection := ai.FlattenCanonicalText(first)
	if strings.Contains(projection, "provider secret") {
		t.Fatalf("canonical output leaked backend error: %s", projection)
	}
}

func TestEnricherHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	renderer := &fakeBaselineRenderer{baseline: validBaseline(), started: make(chan struct{}, 1), release: release}
	enricher, media, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(ctx, testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
		result <- err
	}()
	<-renderer.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Enrich cancellation error = %v, want context.Canceled", err)
	}
	if len(media.inputs) != 1 {
		t.Fatalf("original was not persisted before rendering: %d persists", len(media.inputs))
	}
}

func TestEnricherLimitsConcurrencyToTwo(t *testing.T) {
	release := make(chan struct{})
	renderer := &fakeBaselineRenderer{baseline: validBaseline(), started: make(chan struct{}, 3), release: release}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
		result <- err
	}()
	<-renderer.started
	<-renderer.started
	if got := renderer.max.Load(); got != MaxConcurrentEnrichments {
		t.Fatalf("concurrent baselines = %d, want %d", got, MaxConcurrentEnrichments)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("Enrich: %v", err)
	}
}

func TestEnricherDeadlineDoesNotWaitForUncooperativeRenderer(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	renderer := &uncooperativeRenderer{started: make(chan struct{}, 1), release: release}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{MessageTimeout: 40 * time.Millisecond})
	started := time.Now()
	result := make(chan struct {
		blocks []ai.ContentBlock
		err    error
	}, 1)
	go func() {
		blocks, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
		result <- struct {
			blocks []ai.ContentBlock
			err    error
		}{blocks, err}
	}()
	<-renderer.started
	got := <-result
	if got.err != nil {
		t.Fatalf("Enrich: %v", got.err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Enrich waited for uncooperative renderer: %s", elapsed)
	}
	if baseline := got.blocks[0].(ai.ImageRefContent).Baseline; baseline != (ai.ImageBaseline{}) {
		t.Fatalf("deadline baseline = %+v, want unavailable", baseline)
	}
}

func TestEnricherLeavesAtMostTwoUncooperativeRenderers(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	renderer := &uncooperativeRenderer{started: make(chan struct{}, MaxConcurrentEnrichments+1), release: release}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{MessageTimeout: 40 * time.Millisecond})
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
		result <- err
	}()
	<-renderer.started
	<-renderer.started
	if err := <-result; err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got := renderer.max.Load(); got != MaxConcurrentEnrichments {
		t.Fatalf("uncooperative active renderers = %d, want %d", got, MaxConcurrentEnrichments)
	}
}

func TestEnricherUsesOneTotalDeadline(t *testing.T) {
	renderer := &fakeBaselineRenderer{err: errors.New("slow renderer"), release: make(chan struct{})}
	enricher, _, _ := newFakeEnricher(t, renderer, PipelineOptions{MessageTimeout: 40 * time.Millisecond})
	started := time.Now()
	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Enrich exceeded shared deadline: %s", elapsed)
	}
	if got := out[0].(ai.ImageRefContent).Baseline; got != (ai.ImageBaseline{}) {
		t.Fatalf("deadline baseline = %#v, want unavailable", got)
	}
}

func TestPrepareTasksBoundsImageCountAndAggregateBytes(t *testing.T) {
	blocks := make([]ai.ContentBlock, ai.MaxImagesPerMessage+1)
	for i := range blocks {
		blocks[i] = imageBlock(t, byte(i))
	}
	if _, err := prepareTasks(blocks, testOwner()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("image count error = %v", err)
	}
	chunk := ai.ImageContent{Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(21*1024*1024)), MimeType: "image/png"}
	if _, err := prepareTasks([]ai.ContentBlock{chunk, chunk, chunk}, testOwner()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("aggregate error = %v", err)
	}
}

type benchmarkPersister struct {
	bytes atomic.Int64
}

func (p *benchmarkPersister) Load(context.Context, Owner, string) (ai.ImageContent, error) {
	return ai.ImageContent{}, ErrNotFound
}

func (p *benchmarkPersister) Persist(_ context.Context, in Input) (string, ai.ImageBaseline, error) {
	p.bytes.Add(int64(len(in.Data)))
	return "benchmark-media", ai.ImageBaseline{}, nil
}

func (p *benchmarkPersister) Baselines(context.Context, Owner, []string) (map[string]ai.ImageBaseline, error) {
	return nil, nil
}

func (p *benchmarkPersister) StoreBaseline(_ context.Context, _ Owner, _ string, baseline ai.ImageBaseline) (ai.ImageBaseline, error) {
	return baseline, nil
}

// BenchmarkEnricherBaseline records the deterministic in-process cost of one
// immutable baseline pipeline. The fake renderer makes this repeatable and
// avoids pretending to measure external provider or local-model latency.
func BenchmarkEnricherBaseline(b *testing.B) {
	persister := &benchmarkPersister{}
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, err := newEnricher(persister, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer
	}), PipelineOptions{})
	if err != nil {
		b.Fatal(err)
	}
	blocks := []ai.ContentBlock{imageBlock(b, 1)}
	owner := testOwner()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := enricher.Enrich(context.Background(), owner, "agent", blocks); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(1, "baselines/op")
	b.ReportMetric(float64(persister.bytes.Load())/float64(b.N), "persisted_bytes/op")
}

// The whole point of moving the baseline onto ctx_media: the same picture
// forwarded into a second message is described once, not once per message.
func TestEnricherRendersOneBaselinePerMediaObject(t *testing.T) {
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, media, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	owner := testOwner()
	image := imageBlock(t, 1)

	first, err := enricher.Enrich(context.Background(), owner, "agent", []ai.ContentBlock{image})
	if err != nil {
		t.Fatalf("first Enrich: %v", err)
	}
	second, err := enricher.Enrich(context.Background(), owner, "agent", []ai.ContentBlock{ai.TextContent{Text: "forwarded"}, image})
	if err != nil {
		t.Fatalf("second Enrich: %v", err)
	}

	firstRef := first[0].(ai.ImageRefContent)
	secondRef := second[1].(ai.ImageRefContent)
	if firstRef != secondRef {
		t.Fatalf("forwarded refs differ: %+v / %+v", firstRef, secondRef)
	}
	if firstRef.Baseline != validBaseline() {
		t.Fatalf("baseline = %+v, want the rendered description", firstRef.Baseline)
	}
	if got := renderer.calls.Load(); got != 1 {
		t.Fatalf("renders = %d, want exactly one per media object", got)
	}
	if got := media.baselines[firstRef.MediaID]; got != validBaseline() {
		t.Fatalf("stored baseline = %+v, want the render written back to the media row", got)
	}
}

// Re-ingesting bytes that were already described skips the VLM entirely: the
// media row hands the description back at persist time.
func TestEnricherSkipsRenderWhenPersistFindsAStoredBaseline(t *testing.T) {
	winner := ai.ImageBaseline{Text: "## Text\nwinner\n\n## Scene\nthe description that landed first"}
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, media, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	owner := testOwner()
	image := imageBlock(t, 1)

	// Seed the row the way a concurrent reader that got there first would leave
	// it: the object exists under a different media ID than this enricher would
	// mint, so the write loses the race rather than being skipped up front.
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		t.Fatal(err)
	}
	media.seedBaseline(owner, "media-raced", data, image.MimeType, winner)

	out, err := enricher.Enrich(context.Background(), owner, "agent", []ai.ContentBlock{image})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	ref := out[0].(ai.ImageRefContent)
	if ref.Baseline != winner {
		t.Fatalf("baseline = %+v, want the description already stored", ref.Baseline)
	}
	if got := renderer.calls.Load(); got != 0 {
		t.Fatalf("renders = %d, want none: the object was already described", got)
	}
}

// A group reference whose media object is already described costs neither a
// blob read nor a VLM call: RenderBaselines hydrates before it opens anything.
func TestRenderBaselinesAdoptsStoredBaselineWithoutReadingTheBlob(t *testing.T) {
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, media, _ := newFakeEnricher(t, renderer, PipelineOptions{})
	owner := testOwner()
	stored := ai.ImageBaseline{Text: "## Text\nstored\n\n## Scene\ndescribed by an earlier reader"}
	media.seedBaseline(owner, "media-stored", []byte("image bytes"), "image/png", stored)

	out, err := enricher.RenderBaselines(context.Background(), owner, "agent", []ai.ContentBlock{
		ai.TextContent{Text: "look"},
		ai.ImageRefContent{MediaID: "media-stored"},
	})
	if err != nil {
		t.Fatalf("RenderBaselines: %v", err)
	}
	ref := out[1].(ai.ImageRefContent)
	if ref.Baseline != stored {
		t.Fatalf("baseline = %+v, want the stored description", ref.Baseline)
	}
	if got := media.loads.Load(); got != 0 {
		t.Fatalf("blob loads = %d, want none for an already-described object", got)
	}
	if got := renderer.calls.Load(); got != 0 {
		t.Fatalf("renders = %d, want none for an already-described object", got)
	}
}

// racingRenderer describes the image, but not before another reader has stored
// its own description of the same media object. That is the interleaving
// SetMediaBaselineIfAbsent exists for: the write affects zero rows and the loser
// must adopt what landed instead of overwriting it.
type racingRenderer struct {
	media    *fakePersister
	winner   ai.ImageBaseline
	rendered ai.ImageBaseline
}

func (r *racingRenderer) Baseline(_ context.Context, _ vision.Request) (ai.ImageBaseline, error) {
	r.media.mu.Lock()
	for mediaID := range r.media.objects {
		r.media.baselines[mediaID] = r.winner
	}
	r.media.mu.Unlock()
	return r.rendered, nil
}

func TestEnricherAdoptsTheBaselineThatWonTheRace(t *testing.T) {
	media := &fakePersister{}
	renderer := &racingRenderer{media: media, winner: ai.ImageBaseline{Text: "## Text\nwinner\n\n## Scene\nthe description that landed first"}, rendered: validBaseline()}
	enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer
	}), PipelineOptions{})
	if err != nil {
		t.Fatalf("newEnricher: %v", err)
	}

	out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	ref := out[0].(ai.ImageRefContent)
	if ref.Baseline != renderer.winner {
		t.Fatalf("baseline = %+v, want the description that won the race", ref.Baseline)
	}
	if got := media.baselines[ref.MediaID]; got != renderer.winner {
		t.Fatalf("stored baseline = %+v, want the winner left untouched", got)
	}
}

// A baseline that never reached its row must not reach the transcript either.
// The direct-session path writes the returned description into the immutable
// ctx_message projection, so a render kept after a failed write would leave that
// message describing an image whose row still says "never described", forever.
func TestEnricherDropsABaselineThatDidNotReachItsRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media func() *fakePersister
	}{
		{name: "write failed", media: func() *fakePersister {
			return &fakePersister{storeErr: errors.New("ctx_media unavailable")}
		}},
		{name: "write matched no row", media: func() *fakePersister {
			// A store that silently keeps nothing stands in for a media row that
			// is gone or was never this owner's: affected=0 and the re-read finds
			// nothing.
			return &fakePersister{amnesiac: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := tc.media()
			renderer := &fakeBaselineRenderer{baseline: validBaseline()}
			enricher, err := newEnricher(media, visionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
				return renderer
			}), PipelineOptions{})
			if err != nil {
				t.Fatalf("newEnricher: %v", err)
			}

			out, err := enricher.Enrich(context.Background(), testOwner(), "agent", []ai.ContentBlock{imageBlock(t, 3)})
			if err != nil {
				t.Fatalf("Enrich: %v", err)
			}
			ref, ok := out[0].(ai.ImageRefContent)
			if !ok || ref.MediaID == "" {
				t.Fatalf("block = %#v, want a stored reference", out[0])
			}
			if ref.Baseline.Text != "" {
				t.Fatalf("baseline = %q, want none: it never reached the row", ref.Baseline.Text)
			}
			// The message still projects, as the unavailable marker rather than
			// as a description nothing backs.
			if got := ai.FlattenCanonicalText(out); !strings.Contains(got, ai.UnavailableImageProjection) {
				t.Fatalf("projection = %q, want the unavailable marker", got)
			}
			if renderer.calls.Load() != 1 {
				t.Fatalf("renders = %d, want exactly one attempt", renderer.calls.Load())
			}
		})
	}
}
