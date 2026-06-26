//go:build xberg && cgo

package document

import (
	"context"
	"fmt"

	xberg "github.com/xberg-io/xberg"
)

type xbergExtractor struct{}

func newBaseExtractor() Extractor {
	return xbergExtractor{}
}

func (xbergExtractor) ExtractFile(ctx context.Context, path string, opts Options) (*Result, error) {
	return runXberg(ctx, opts, func() (*xberg.ExtractionResult, error) {
		return xberg.ExtractFileSync(path, nil, xbergConfig(opts))
	})
}

func (xbergExtractor) ExtractBytes(ctx context.Context, data []byte, mime string, opts Options) (*Result, error) {
	return runXberg(ctx, opts, func() (*xberg.ExtractionResult, error) {
		return xberg.ExtractBytesSync(data, mime, xbergConfig(opts))
	})
}

func runXberg(ctx context.Context, opts Options, fn func() (*xberg.ExtractionResult, error)) (*Result, error) {
	cctx, cancel := WithTimeout(ctx, opts.Timeout)
	defer cancel()

	type outcome struct {
		result *xberg.ExtractionResult
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		result, err := fn()
		ch <- outcome{result: result, err: err}
	}()

	select {
	case <-cctx.Done():
		// Synchronous cgo calls cannot be interrupted from Go; the goroutine may
		// finish later. If this becomes the default path, move xberg behind a helper
		// process for hard cancellation and crash isolation.
		return nil, cctx.Err()
	case out := <-ch:
		if out.err != nil {
			return nil, fmt.Errorf("extract document with xberg: %w", out.err)
		}
		return NormalizeResult(&Result{Content: out.result.Content, MimeType: out.result.MimeType})
	}
}

func xbergConfig(opts Options) xberg.ExtractionConfig {
	cfg := xberg.ExtractionConfig{DisableOcr: true}
	if opts.Timeout > 0 {
		secs := uint64(opts.Timeout.Seconds())
		if secs == 0 {
			secs = 1
		}
		cfg.ExtractionTimeoutSecs = &secs
	}
	return cfg
}
