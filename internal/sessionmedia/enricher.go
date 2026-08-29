package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	MessageEnrichmentTimeout = 15 * time.Second
	MaxConcurrentEnrichments = 2
)

// mediaOps is the storage half of the pipeline. Persist mints references and
// reports any description these exact bytes already carry; Load reopens an
// already-referenced original so a baseline can be rendered later than the
// message that carried it; Baselines and StoreBaseline make the description a
// property of the media object, rendered once per (owner, sha256).
type mediaOps interface {
	Persist(context.Context, Input) (string, ai.ImageBaseline, error)
	Load(context.Context, Owner, string) (ai.ImageContent, error)
	Baselines(context.Context, Owner, []string) (map[string]ai.ImageBaseline, error)
	StoreBaseline(context.Context, Owner, string, ai.ImageBaseline) (ai.ImageBaseline, error)
}

// visionFactory resolves current deployment settings once per message.
type visionFactory interface {
	ForMessage(context.Context, string) vision.BaselineRenderer
}

type visionFactoryFunc func(context.Context, string) vision.BaselineRenderer

func (f visionFactoryFunc) ForMessage(ctx context.Context, agentID string) vision.BaselineRenderer {
	return f(ctx, agentID)
}

// PipelineOptions makes deadlines and concurrency deterministic in tests.
// Production uses zero-value defaults.
type PipelineOptions struct {
	MessageTimeout time.Duration
	MaxConcurrent  int
}

// enricher changes ephemeral provider/tool images into canonical references.
// It persists immutable originals but leaves atomic message/part append to the
// memory module.
type enricher struct {
	media   mediaOps
	vision  visionFactory
	timeout time.Duration
	workers int
}

// newEnricher receives a message-scoped factory that owns the complete
// VLM → Xberg fallback ladder.
func newEnricher(media mediaOps, factory visionFactory, opts PipelineOptions) (*enricher, error) {
	if media == nil || factory == nil {
		return nil, fmt.Errorf("session media enricher: %w", ErrInvalidInput)
	}
	if opts.MessageTimeout == 0 {
		opts.MessageTimeout = MessageEnrichmentTimeout
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = MaxConcurrentEnrichments
	}
	if opts.MessageTimeout <= 0 || opts.MaxConcurrent <= 0 || opts.MaxConcurrent > MaxConcurrentEnrichments {
		return nil, fmt.Errorf("session media enricher: %w", ErrInvalidInput)
	}
	return &enricher{media: media, vision: factory, timeout: opts.MessageTimeout, workers: opts.MaxConcurrent}, nil
}

// Enrich runs one bounded, ordered pipeline: validate and deduplicate all raw
// blocks; persist every unique original; resolve current deployment settings
// once; then render baselines. Factory failure falls back to local Xberg;
// renderer failures become stable unavailable values only after the immutable
// original exists.
func (e *enricher) Enrich(ctx context.Context, owner Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	out, tasks, err := prepare(owner, blocks)
	if err != nil || len(tasks) == 0 {
		return out, err
	}

	messageCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	if err := e.persistTasks(ctx, messageCtx, tasks); err != nil {
		return nil, err
	}
	if messageCtx.Err() != nil {
		return e.assemble(ctx, out, tasks)
	}

	renderer := e.vision.ForMessage(messageCtx, agentID)
	if renderer == nil {
		return e.assemble(ctx, out, tasks)
	}

	e.runTasks(messageCtx, tasks, func(ctx context.Context, task *enrichmentTask) {
		e.renderOne(ctx, renderer, task)
	})
	return e.assemble(ctx, out, tasks)
}

// Persist is the store-only half of Enrich: the original becomes immutable and
// the block becomes a reference with no baseline yet. Group ingestion uses it
// so an image no turn ever wakes on never costs a VLM call; the baseline is
// rendered later, at most once, by RenderBaselines.
func (e *enricher) Persist(ctx context.Context, owner Owner, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	out, tasks, err := prepare(owner, blocks)
	if err != nil || len(tasks) == 0 {
		return out, err
	}
	messageCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	if err := e.persistTasks(ctx, messageCtx, tasks); err != nil {
		return nil, err
	}
	return e.assemble(ctx, out, tasks)
}

// RenderBaselines fills in the missing baselines of already-persisted
// references. Descriptions already stored on the media objects are adopted
// first, so a forwarded image costs neither a blob read nor a VLM call; only
// what is left is rendered. It is best effort by construction: a reference whose
// original cannot be reopened, or whose render fails or runs out of time, is
// returned unchanged so the turn continues on provider bytes and a later turn
// can retry.
func (e *enricher) RenderBaselines(ctx context.Context, owner Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	if !owner.Valid() {
		return nil, fmt.Errorf("session media render: %w", ErrInvalidInput)
	}
	out := ai.CloneContentBlocks(blocks)
	tasks := baselineTasks(out, owner)
	if len(tasks) == 0 {
		return out, nil
	}

	messageCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	pending := e.hydrateBaselines(messageCtx, owner, tasks)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		e.runTasks(messageCtx, pending, e.loadOne)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if renderer := e.vision.ForMessage(messageCtx, agentID); renderer != nil && messageCtx.Err() == nil {
			e.runTasks(messageCtx, pending, func(ctx context.Context, task *enrichmentTask) {
				if task.err != nil {
					return
				}
				e.renderOne(ctx, renderer, task)
			})
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	for _, task := range tasks {
		if task.err != nil || task.ref.Baseline.Text == "" {
			continue
		}
		for _, index := range task.indexes {
			out[index] = task.ref
		}
	}
	return out, nil
}

// persistTasks bounds the persistence stage. messageCtx carries the wall-clock
// deadline; ctx is the caller's, so a cancelled request never reports success.
func (e *enricher) persistTasks(ctx, messageCtx context.Context, tasks []*enrichmentTask) error {
	e.runTasks(messageCtx, tasks, e.persistOne)
	if err := ctx.Err(); err != nil {
		return err
	}
	return persistedTasksError(tasks)
}

// runTasks bounds both pipeline stages and never blocks a dispatcher after
// cancellation. Workers must honor ctx for a strict wall-clock bound.
func (e *enricher) runTasks(ctx context.Context, tasks []*enrichmentTask, run func(context.Context, *enrichmentTask)) {
	jobs := make(chan *enrichmentTask)
	var workers sync.WaitGroup
	for range e.workers {
		workers.Go(func() {
			for task := range jobs {
				// A dispatcher and deadline can become ready together. Never start
				// another persistence/render operation after the deadline wins.
				if ctx.Err() == nil {
					run(ctx, task)
				}
			}
		})
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			close(jobs)
			workers.Wait()
			return
		}
		select {
		case jobs <- task:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

type enrichmentTask struct {
	indexes []int
	input   Input
	req     vision.Request
	ref     ai.ImageRefContent
	// stored is true when ref.Baseline came from ctx_media rather than from a
	// render on this turn, so it must not be written back.
	stored bool
	err    error
}

// prepare clones the block list and derives one task per distinct raw image.
func prepare(owner Owner, blocks []ai.ContentBlock) ([]ai.ContentBlock, []*enrichmentTask, error) {
	if !owner.Valid() {
		return nil, nil, fmt.Errorf("session media enrich: %w", ErrInvalidInput)
	}
	out := ai.CloneContentBlocks(blocks)
	tasks, err := prepareTasks(blocks, owner)
	if err != nil {
		return nil, nil, err
	}
	return out, tasks, nil
}

// baselineTasks derives one task per distinct reference still missing a
// baseline. References that already carry one are never rendered again.
func baselineTasks(blocks []ai.ContentBlock, owner Owner) []*enrichmentTask {
	byMedia := make(map[string]*enrichmentTask)
	tasks := make([]*enrichmentTask, 0)
	for index, block := range blocks {
		ref, ok := block.(ai.ImageRefContent)
		if !ok || ref.Baseline.Text != "" || ref.Validate() != nil {
			continue
		}
		if task := byMedia[ref.MediaID]; task != nil {
			task.indexes = append(task.indexes, index)
			continue
		}
		task := &enrichmentTask{indexes: []int{index}, input: Input{Owner: owner}, ref: ref}
		byMedia[ref.MediaID] = task
		tasks = append(tasks, task)
	}
	return tasks
}

func prepareTasks(blocks []ai.ContentBlock, owner Owner) ([]*enrichmentTask, error) {
	byDigest := make(map[[sha256.Size]byte]*enrichmentTask)
	tasks := make([]*enrichmentTask, 0)
	imageCount := 0
	aggregateBytes := 0
	// Preflight every encoded block before Base64 decoding allocates any image.
	for index, block := range blocks {
		imageBlock, ok := block.(ai.ImageContent)
		if !ok {
			continue
		}
		imageCount++
		if imageCount > ai.MaxImagesPerMessage {
			return nil, fmt.Errorf("session media: %w: more than %d images", ErrInvalidInput, ai.MaxImagesPerMessage)
		}
		decodedLen := base64.StdEncoding.DecodedLen(len(imageBlock.Data))
		if decodedLen > ai.MaxImageInputBytes {
			return nil, fmt.Errorf("session media image %d: decoded input exceeds %d bytes", index, ai.MaxImageInputBytes)
		}
		aggregateBytes += decodedLen
		if aggregateBytes > ai.MaxAggregateImageBytes {
			return nil, fmt.Errorf("session media: %w: decoded images exceed %d bytes", ErrInvalidInput, ai.MaxAggregateImageBytes)
		}
	}
	for index, block := range blocks {
		imageBlock, ok := block.(ai.ImageContent)
		if !ok {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(imageBlock.Data)
		if err != nil {
			return nil, fmt.Errorf("session media image %d: decode base64: %w", index, err)
		}
		_, mime, err := vision.ValidateImage(data, imageBlock.MimeType)
		if err != nil {
			return nil, fmt.Errorf("session media image %d: %w", index, err)
		}
		digest := sha256.Sum256(data)
		if task := byDigest[digest]; task != nil {
			task.indexes = append(task.indexes, index)
			continue
		}
		task := &enrichmentTask{
			indexes: []int{index},
			input:   Input{Owner: owner, Data: data, MimeType: mime},
			req:     vision.Request{Data: data, MimeType: mime},
		}
		byDigest[digest] = task
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (e *enricher) persistOne(ctx context.Context, task *enrichmentTask) {
	mediaID, baseline, err := e.media.Persist(ctx, task.input)
	if err != nil {
		task.err = fmt.Errorf("persist canonical session media: %w", err)
		return
	}
	if mediaID == "" {
		task.err = fmt.Errorf("persist canonical session media: invalid media result")
		return
	}
	task.ref.MediaID = mediaID
	// These bytes have been described before, under this same owner. Adopting
	// that description is the whole point of keying the baseline on the media
	// object: renderOne will skip the VLM for this task.
	if baseline.Text != "" {
		task.ref.Baseline = baseline
		task.stored = true
	}
}

// loadOne reopens one immutable original as the renderer request payload.
func (e *enricher) loadOne(ctx context.Context, task *enrichmentTask) {
	image, err := e.media.Load(ctx, task.input.Owner, task.ref.MediaID)
	if err != nil {
		task.err = fmt.Errorf("load canonical session media: %w", err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		task.err = fmt.Errorf("decode canonical session media: %w", err)
		return
	}
	task.req = vision.Request{Data: data, MimeType: image.MimeType}
}

func persistedTasksError(tasks []*enrichmentTask) error {
	for _, task := range tasks {
		if task.err != nil {
			return task.err
		}
		if task.ref.MediaID == "" {
			return context.DeadlineExceeded
		}
	}
	return nil
}

// renderOne describes one image and records the description on its media row.
// A task that already carries a stored baseline is skipped: the media object has
// been described, and describing it again would only produce a second, equally
// true paragraph at the price of a VLM call.
func (e *enricher) renderOne(ctx context.Context, renderer vision.BaselineRenderer, task *enrichmentTask) {
	if task.stored {
		return
	}
	type result struct {
		baseline ai.ImageBaseline
		err      error
	}
	// The channel is buffered so a renderer that outlives ctx never blocks or
	// mutates task state after enrichment has returned.
	results := make(chan result, 1)
	go func() {
		baseline, err := renderer.Baseline(ctx, task.req)
		results <- result{baseline: baseline, err: err}
	}()

	select {
	case outcome := <-results:
		if outcome.err != nil || ctx.Err() != nil || !validBaselineResult(outcome.baseline) {
			return
		}
		task.ref.Baseline = e.commitBaseline(ctx, task, outcome.baseline)
	case <-ctx.Done():
		return
	}
}

// commitBaseline makes this render the media object's description, or adopts the
// one that got there first. A baseline that did not reach the row is discarded,
// not returned: the direct-session path writes what comes back into the
// immutable ctx_message projection, so returning an uncommitted render would
// leave that message permanently describing an image whose row still says
// "never described". Dropping it degrades this turn to the unavailable marker
// and leaves the next reader free to describe the image for real.
func (e *enricher) commitBaseline(ctx context.Context, task *enrichmentTask, rendered ai.ImageBaseline) ai.ImageBaseline {
	stored, err := e.media.StoreBaseline(ctx, task.input.Owner, task.ref.MediaID, rendered)
	if err != nil {
		slog.Warn("store session media baseline failed", "media_id", task.ref.MediaID, "error", err)
		return ai.ImageBaseline{}
	}
	if stored.Text == "" {
		// The write matched no row and the re-read found nothing: this owner's
		// media object is gone or was never theirs. Nothing landed.
		slog.Warn("session media baseline did not reach its row", "media_id", task.ref.MediaID)
		return ai.ImageBaseline{}
	}
	task.stored = true
	return stored
}

// hydrateBaselines fills in the descriptions ctx_media already holds and reports
// the tasks still needing one. It runs before any blob is opened, so a reference
// whose media was described by an earlier reader costs neither a blob read nor a
// VLM call.
func (e *enricher) hydrateBaselines(ctx context.Context, owner Owner, tasks []*enrichmentTask) []*enrichmentTask {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ref.MediaID)
	}
	stored, err := e.media.Baselines(ctx, owner, ids)
	if err != nil {
		// Losing the lookup only costs a re-render, so the turn continues.
		slog.Warn("load session media baselines failed", "owner_kind", owner.Kind, "error", err)
		return tasks
	}
	pending := make([]*enrichmentTask, 0, len(tasks))
	for _, task := range tasks {
		if baseline, ok := stored[task.ref.MediaID]; ok {
			task.ref.Baseline = baseline
			task.stored = true
			continue
		}
		pending = append(pending, task)
	}
	return pending
}

func (e *enricher) assemble(ctx context.Context, out []ai.ContentBlock, tasks []*enrichmentTask) ([]ai.ContentBlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, task := range tasks {
		for _, index := range task.indexes {
			out[index] = task.ref
		}
	}
	return out, nil
}

func validBaselineResult(result ai.ImageBaseline) bool {
	return result.Text != "" && result.Validate() == nil
}
