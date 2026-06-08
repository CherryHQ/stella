package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
)

type teeHandler struct {
	handlers []slog.Handler
}

func currentSlogHandler() slog.Handler {
	handler := slog.Default().Handler()
	// The package-level default handler writes through the standard log package.
	// slog.SetDefault redirects that package back into slog, so teeing it would
	// recurse. stellad installs its own TextHandler before Init; this fallback is
	// for tests and direct package use.
	if reflect.TypeOf(handler).String() == "*slog.defaultHandler" {
		return slog.NewTextHandler(os.Stderr, nil)
	}
	return handler
}

func newTeeHandler(handlers ...slog.Handler) slog.Handler {
	return teeHandler{handlers: handlers}
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if len(h.handlers) == 0 {
		return false
	}
	return h.handlers[0].Enabled(ctx, level)
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			err = errors.Join(err, handler.Handle(ctx, record.Clone()))
		}
	}
	return err
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return teeHandler{handlers: handlers}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return teeHandler{handlers: handlers}
}
