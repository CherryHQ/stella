package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	MessageEnrichmentTimeout = 15 * time.Second
	MaxConcurrentEnrichments = 2
)

type persister interface {
	Persist(context.Context, Input) (string, error)
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
	media   persister
	vision  visionFactory
	timeout time.Duration
	workers int
}

// newEnricher receives a message-scoped factory that owns the complete
// VLM → Xberg fallback ladder.
func newEnricher(media persister, factory visionFactory, opts PipelineOptions) (*enricher, error) {
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
func (e *enricher) Enrich(ctx context.Context, userID uuid.UUID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("session media enrich: %w", ErrInvalidInput)
	}
	out := ai.CloneContentBlocks(blocks)
	tasks, err := prepareTasks(blocks, userID)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return out, nil
	}

	messageCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	e.runTasks(messageCtx, tasks, e.persistOne)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := persistedTasksError(tasks); err != nil {
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

func prepareTasks(blocks []ai.ContentBlock, userID uuid.UUID) ([]*enrichmentTask, error) {
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
			input:   Input{UserID: userID, Data: data, MimeType: mime},
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
