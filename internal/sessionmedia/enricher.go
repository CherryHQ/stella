package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	MessageEnrichmentTimeout = 15 * time.Second
	MaxConcurrentEnrichments = 2
)

// mediaOps is the storage half of the pipeline. Persist mints references;
// Load reopens an already-referenced original so a baseline can be rendered
// later than the message that carried it.
type mediaOps interface {
	Persist(context.Context, Input) (string, error)
	Load(context.Context, Owner, string) (ai.ImageContent, error)
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
// references. It is best effort by construction: a reference whose original
// cannot be reopened, or whose render fails or runs out of time, is returned
// unchanged so the turn continues on provider bytes and a later turn can retry.
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

	e.runTasks(messageCtx, tasks, e.loadOne)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	renderer := e.vision.ForMessage(messageCtx, agentID)
	if renderer == nil || messageCtx.Err() != nil {
		return out, nil
	}
	e.runTasks(messageCtx, tasks, func(ctx context.Context, task *enrichmentTask) {
		if task.err != nil {
			return
		}
		e.renderOne(ctx, renderer, task)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
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
	err     error
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
	mediaID, err := e.media.Persist(ctx, task.input)
	if err != nil {
		task.err = fmt.Errorf("persist canonical session media: %w", err)
		return
	}
	if mediaID == "" {
		task.err = fmt.Errorf("persist canonical session media: invalid media result")
		return
	}
	task.ref.MediaID = mediaID
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

func (e *enricher) renderOne(ctx context.Context, renderer vision.BaselineRenderer, task *enrichmentTask) {
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
		task.ref.Baseline = outcome.baseline
	case <-ctx.Done():
		return
	}
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
