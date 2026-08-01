package sessionmedia

import (
	"bytes"
	"context"
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

type fakePersister struct {
	mu      sync.Mutex
	inputs  []Input
	started chan struct{}
	release <-chan struct{}
	active  atomic.Int64
	max     atomic.Int64
}

func (p *fakePersister) Persist(ctx context.Context, in Input) (string, error) {
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
			return "", ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, in)
	return fmt.Sprintf("media-%d", len(p.inputs)), nil
}

type fakeBaselineRenderer struct {
	baseline ai.ImageBaseline
	err      error
	started  chan struct{}
	release  <-chan struct{}
	active   atomic.Int64
	max      atomic.Int64
}

func (r *fakeBaselineRenderer) Baseline(ctx context.Context, _ vision.Request) (ai.ImageBaseline, error) {
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

func newFakeEnricher(t testing.TB, renderer vision.BaselineRenderer, opts EnricherOptions) (*Enricher, *fakePersister, *atomic.Int64) {
	t.Helper()
	media := &fakePersister{}
	var resolves atomic.Int64
	enricher, err := NewEnricher(media, VisionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		resolves.Add(1)
		return renderer
	}), opts)
	if err != nil {
		t.Fatalf("NewEnricher: %v", err)
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
	factory, err := NewSnapshotVisionFactory(loader, func(_, _, _ string) (providers.StreamFunc, error) {
		builds.Add(1)
		return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
			return nil, errors.New("not called by factory test")
		}, nil
	})
	if err != nil {
		t.Fatalf("NewSnapshotVisionFactory: %v", err)
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
	enricher, media, resolves := newFakeEnricher(t, renderer, EnricherOptions{})
	user := []ai.ContentBlock{ai.TextContent{Text: "user text"}, imageBlock(t, 1)}
	tool := []ai.ContentBlock{imageBlock(t, 2), ai.TextContent{Text: "tool text"}}

	userOut, err := enricher.Enrich(context.Background(), uuid.New(), "agent", user)
	if err != nil {
		t.Fatalf("enrich user: %v", err)
	}
	toolOut, err := enricher.Enrich(context.Background(), uuid.New(), "agent", tool)
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
	enricher, err := NewEnricher(media, VisionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		media.mu.Lock()
		persisted := len(media.inputs)
		media.mu.Unlock()
		if persisted != 3 {
			t.Errorf("factory ran after %d persists, want all 3", persisted)
		}
		resolved.Store(true)
		return renderer
	}), EnricherOptions{})
	if err != nil {
		t.Fatalf("NewEnricher: %v", err)
	}
	out, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
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
	enricher, err := NewEnricher(media, VisionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer
	}), EnricherOptions{})
	if err != nil {
		t.Fatalf("NewEnricher: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
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
	enricher, err := NewEnricher(media, VisionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return nil
	}), EnricherOptions{})
	if err != nil {
		t.Fatalf("NewEnricher: %v", err)
	}
	out, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
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
	enricher, media, _ := newFakeEnricher(t, renderer, EnricherOptions{})
	image := imageBlock(t, 1)
	out, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{image, ai.TextContent{Text: "between"}, image})
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
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{})
	out, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got := out[0].(ai.ImageRefContent).Baseline; got != (ai.ImageBaseline{}) {
		t.Fatalf("malformed renderer baseline = %+v, want stable unavailable", got)
	}
}

func TestEnricherUnavailableIsStableAndDoesNotStoreBackendError(t *testing.T) {
	renderer := &fakeBaselineRenderer{err: errors.New("provider secret backend error")}
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{})
	input := []ai.ContentBlock{imageBlock(t, 1)}
	first, err := enricher.Enrich(context.Background(), uuid.New(), "agent", input)
	if err != nil {
		t.Fatalf("first Enrich: %v", err)
	}
	second, err := enricher.Enrich(context.Background(), uuid.New(), "agent", input)
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
	enricher, media, _ := newFakeEnricher(t, renderer, EnricherOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(ctx, uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
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
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{})
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
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
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{MessageTimeout: 40 * time.Millisecond})
	started := time.Now()
	result := make(chan struct {
		blocks []ai.ContentBlock
		err    error
	}, 1)
	go func() {
		blocks, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
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
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{MessageTimeout: 40 * time.Millisecond})
	result := make(chan error, 1)
	go func() {
		_, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1), imageBlock(t, 2), imageBlock(t, 3)})
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
	enricher, _, _ := newFakeEnricher(t, renderer, EnricherOptions{MessageTimeout: 40 * time.Millisecond})
	started := time.Now()
	out, err := enricher.Enrich(context.Background(), uuid.New(), "agent", []ai.ContentBlock{imageBlock(t, 1)})
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
	blocks := make([]ai.ContentBlock, MaxImagesPerMessage+1)
	for i := range blocks {
		blocks[i] = imageBlock(t, byte(i))
	}
	if _, err := prepareTasks(blocks, uuid.New()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("image count error = %v", err)
	}
	chunk := ai.ImageContent{Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(21*1024*1024)), MimeType: "image/png"}
	if _, err := prepareTasks([]ai.ContentBlock{chunk, chunk, chunk}, uuid.New()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("aggregate error = %v", err)
	}
}

type benchmarkPersister struct {
	bytes atomic.Int64
}

func (p *benchmarkPersister) Persist(_ context.Context, in Input) (string, error) {
	p.bytes.Add(int64(len(in.Data)))
	return "benchmark-media", nil
}

// BenchmarkEnricherBaseline records the deterministic in-process cost of one
// immutable baseline pipeline. The fake renderer makes this repeatable and
// avoids pretending to measure external provider or local-model latency.
func BenchmarkEnricherBaseline(b *testing.B) {
	persister := &benchmarkPersister{}
	renderer := &fakeBaselineRenderer{baseline: validBaseline()}
	enricher, err := NewEnricher(persister, VisionFactoryFunc(func(context.Context, string) vision.BaselineRenderer {
		return renderer
	}), EnricherOptions{})
	if err != nil {
		b.Fatal(err)
	}
	blocks := []ai.ContentBlock{imageBlock(b, 1)}
	userID := uuid.New()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := enricher.Enrich(context.Background(), userID, "agent", blocks); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(1, "baselines/op")
	b.ReportMetric(float64(persister.bytes.Load())/float64(b.N), "persisted_bytes/op")
}
