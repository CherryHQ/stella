package sessionmedia

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	MessageEnrichmentTimeout = 15 * time.Second
	MaxConcurrentEnrichments = 2
)

// Persister is the narrow immutable-media boundary used by enrichment.
type Persister interface {
	Persist(context.Context, Input) (sqlc.CtxMedium, error)
}

// VisionFactory resolves the deployment's current vision setting once per
// message. It is intentionally independent of runner construction so a setting
// update affects the next image-bearing message without runner eviction.
type VisionFactory interface {
	ForMessage(context.Context, string) (vision.BaselineRenderer, error)
}

// VisionFactoryFunc adapts a function to VisionFactory.
type VisionFactoryFunc func(context.Context, string) (vision.BaselineRenderer, error)

func (f VisionFactoryFunc) ForMessage(ctx context.Context, agentID string) (vision.BaselineRenderer, error) {
	return f(ctx, agentID)
}

// EnricherOptions exists to make time and concurrency behavior deterministic in
// tests. Production uses the zero-value defaults.
type EnricherOptions struct {
	MessageTimeout time.Duration
	MaxConcurrent  int
}

// Enricher changes ephemeral provider/tool images into canonical references.
// It does not write messages or parts; Phase 3 will call it before an atomic
// parent-and-parts append.
type Enricher struct {
	media    Persister
	vision   VisionFactory
	fallback vision.BaselineRenderer
	timeout  time.Duration
	workers  int
}

// NewEnricher requires an Xberg-capable fallback. The message-scoped factory
// supplies the current deployment VLM ladder; when settings cannot resolve,
// the fallback preserves the accepted VLM → Xberg → unavailable behavior.
func NewEnricher(media Persister, factory VisionFactory, fallback vision.BaselineRenderer, opts EnricherOptions) (*Enricher, error) {
	if media == nil || factory == nil || fallback == nil {
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
	return &Enricher{media: media, vision: factory, fallback: fallback, timeout: opts.MessageTimeout, workers: opts.MaxConcurrent}, nil
}

// Enrich runs one bounded, ordered pipeline: validate and deduplicate all raw
// blocks; persist every unique original; resolve current deployment settings
// once; then render baselines. Factory failure falls back to local Xberg;
// renderer failures become stable unavailable values only after the immutable
// original exists.
func (e *Enricher) Enrich(ctx context.Context, userID uuid.UUID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
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
		markUnavailable(tasks)
		return e.assemble(ctx, out, tasks)
	}

	renderer, err := e.vision.ForMessage(messageCtx, agentID)
	if err != nil || renderer == nil {
		renderer = e.fallback
	}

	e.runTasks(messageCtx, tasks, func(ctx context.Context, task *enrichmentTask) {
		e.renderOne(ctx, renderer, task)
	})
	markUnavailable(tasks)
	return e.assemble(ctx, out, tasks)
}

// runTasks bounds both pipeline stages and never blocks a dispatcher after
// cancellation. Workers must honor ctx for a strict wall-clock bound.
func (e *Enricher) runTasks(ctx context.Context, tasks []*enrichmentTask, run func(context.Context, *enrichmentTask)) {
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
	byDigest := make(map[string]*enrichmentTask)
	tasks := make([]*enrichmentTask, 0)
	for index, block := range blocks {
		imageBlock, ok := block.(ai.ImageContent)
		if !ok {
			continue
		}
		if base64.StdEncoding.DecodedLen(len(imageBlock.Data)) > vision.MaxImageInputBytes {
			return nil, fmt.Errorf("session media image %d: decoded input exceeds %d bytes", index, vision.MaxImageInputBytes)
		}
		data, err := base64.StdEncoding.DecodeString(imageBlock.Data)
		if err != nil {
			return nil, fmt.Errorf("session media image %d: decode base64: %w", index, err)
		}
		cfg, mime, err := vision.ValidateImage(data, imageBlock.MimeType)
		if err != nil {
			return nil, fmt.Errorf("session media image %d: %w", index, err)
		}
		digest := sha256.Sum256(data)
		key := mime + ":" + string(digest[:])
		if task := byDigest[key]; task != nil {
			task.indexes = append(task.indexes, index)
			continue
		}
		task := &enrichmentTask{
			indexes: []int{index},
			input:   Input{UserID: userID, Data: data, MimeType: mime, WidthPX: int32(cfg.Width), HeightPX: int32(cfg.Height)},
			req:     vision.Request{Data: data, MimeType: mime},
		}
		byDigest[key] = task
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (e *Enricher) persistOne(ctx context.Context, task *enrichmentTask) {
	media, err := e.media.Persist(ctx, task.input)
	if err != nil {
		task.err = fmt.Errorf("persist canonical session media: %w", err)
		return
	}
	if media.ID == "" || strings.TrimSpace(media.MimeType) == "" {
		task.err = fmt.Errorf("persist canonical session media: invalid media result")
		return
	}
	task.ref.MediaID = media.ID
	task.ref.MimeType = media.MimeType
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

func (e *Enricher) renderOne(ctx context.Context, renderer vision.BaselineRenderer, task *enrichmentTask) {
	type result struct {
		baseline vision.BaselineResult
		err      error
	}
	// The channel is buffered so a renderer that outlives ctx never blocks or
	// mutates task state after Enrich has returned.
	results := make(chan result, 1)
	go func() {
		baseline, err := renderer.Baseline(ctx, task.req)
		results <- result{baseline: baseline, err: err}
	}()

	select {
	case outcome := <-results:
		if outcome.err != nil || ctx.Err() != nil || !validBaselineResult(outcome.baseline) {
			task.ref.Baseline = ai.ImageBaseline{Status: ai.ImageBaselineUnavailable}
			return
		}
		task.ref.Baseline = ai.ImageBaseline{
			Status:   ai.ImageBaselineReady,
			Text:     outcome.baseline.Text,
			Renderer: outcome.baseline.Renderer,
			Contract: outcome.baseline.Contract,
		}
	case <-ctx.Done():
		task.ref.Baseline = ai.ImageBaseline{Status: ai.ImageBaselineUnavailable}
	}
}

func markUnavailable(tasks []*enrichmentTask) {
	for _, task := range tasks {
		if task.ref.Baseline.Status == "" {
			task.ref.Baseline = ai.ImageBaseline{Status: ai.ImageBaselineUnavailable}
		}
	}
}

func (e *Enricher) assemble(ctx context.Context, out []ai.ContentBlock, tasks []*enrichmentTask) ([]ai.ContentBlock, error) {
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

func validBaselineResult(result vision.BaselineResult) bool {
	return result.Contract == ai.ImageBaselineContractV1 &&
		strings.TrimSpace(result.Renderer) != "" &&
		ai.ValidateImageBaselineText(result.Text) == nil
}
