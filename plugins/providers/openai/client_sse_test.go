package openai

import (
	"io"
	"strings"
	"testing"
	"time"
)

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
