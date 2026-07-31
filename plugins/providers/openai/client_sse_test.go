package openai

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestSSECommentStripperReturnsNormalData(t *testing.T) {
	t.Parallel()

	stripper := &sseCommentStripper{
		rc: io.NopCloser(strings.NewReader("data: {\"ok\":true}\n\n")),
	}
	type readResult struct {
		n   int
		err error
	}
	result := make(chan readResult, 1)
	buffer := make([]byte, 64)

	// Run Read asynchronously so the regression reports a timeout instead of hanging the test suite.
	go func() {
		n, err := stripper.Read(buffer)
		result <- readResult{n: n, err: err}
	}()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Read() error = %v", got.err)
		}
		if text := string(buffer[:got.n]); text != "data: {\"ok\":true}\n\n" {
			t.Fatalf("Read() = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("Read() did not return for a normal SSE data event")
	}
}

func TestSSECommentStripperDropsUnterminatedCommentAtEOF(t *testing.T) {
	t.Parallel()

	stripper := &sseCommentStripper{
		rc: io.NopCloser(strings.NewReader(": keep-alive")),
	}
	got, err := io.ReadAll(stripper)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadAll() = %q, want no data", got)
	}
}

func TestSSECommentStripperRejectsOversizedUnterminatedComment(t *testing.T) {
	t.Parallel()

	body := &trackingReadCloser{
		Reader: strings.NewReader(":" + strings.Repeat("x", maxLeadingSSECommentLineBytes)),
	}
	stripper := &sseCommentStripper{rc: body}
	if _, err := io.ReadAll(stripper); !errors.Is(err, errLeadingSSECommentLineTooLong) {
		t.Fatalf("ReadAll() error = %v, want oversized-comment error", err)
	}
	if !body.closed {
		t.Fatal("oversized comment did not close the response body")
	}
}

func TestSSECommentStripperHonorsCancelledRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &trackingReadCloser{Reader: strings.NewReader(": keep-alive\n\ndata: {}\n\n")}
	stripper := &sseCommentStripper{ctx: ctx, rc: body}
	buffer := make([]byte, 16)
	if _, err := stripper.Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context canceled", err)
	}
	if !body.closed {
		t.Fatal("cancelled request did not close the response body")
	}
}
