package main

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/library"
)

type deferredParserFixture struct{ profile string }

func (p deferredParserFixture) Profile(string) (string, error) { return p.profile, nil }
func (deferredParserFixture) Parse(context.Context, string, string) ([]library.ParsedChunk, error) {
	return []library.ParsedChunk{{Content: "ready"}}, nil
}

func TestDeferredLibraryParserActivatesOnce(t *testing.T) {
	t.Parallel()
	parser := &deferredLibraryParser{}
	if _, err := parser.Profile(library.MediaTypePDF); !errors.Is(err, library.ErrServiceUnavailable) {
		t.Fatalf("Profile before activation error = %v, want ErrServiceUnavailable", err)
	}
	if _, err := parser.Parse(t.Context(), "source.pdf", library.MediaTypePDF); !errors.Is(err, library.ErrServiceUnavailable) {
		t.Fatalf("Parse before activation error = %v, want ErrServiceUnavailable", err)
	}
	if !parser.activate(deferredParserFixture{profile: "xberg:v1"}) {
		t.Fatal("first activation failed")
	}
	if parser.activate(deferredParserFixture{profile: "xberg:v2"}) {
		t.Fatal("second activation replaced a durable parser profile")
	}
	profile, err := parser.Profile(library.MediaTypePDF)
	if err != nil || profile != "xberg:v1" {
		t.Fatalf("Profile after activation = %q, %v", profile, err)
	}
	chunks, err := parser.Parse(t.Context(), "source.pdf", library.MediaTypePDF)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "ready" {
		t.Fatalf("Parse after activation = %+v, %v", chunks, err)
	}
}
