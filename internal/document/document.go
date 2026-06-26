package document

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUnavailable       = errors.New("document extraction unavailable")
	ErrUnsupportedFormat = errors.New("unsupported document format")
	ErrEmptyContent      = errors.New("document extraction returned no text")
)

type Options struct {
	Timeout time.Duration
}

type Result struct {
	Content  string
	MimeType string
}

type Extractor interface {
	ExtractFile(ctx context.Context, path string, opts Options) (*Result, error)
	ExtractBytes(ctx context.Context, data []byte, mime string, opts Options) (*Result, error)
}

func NormalizeResult(result *Result) (*Result, error) {
	if result == nil {
		return nil, ErrEmptyContent
	}
	result.Content = strings.TrimSpace(result.Content)
	if result.Content == "" {
		return nil, ErrEmptyContent
	}
	return result, nil
}

func WithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func WrapUnavailable(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
