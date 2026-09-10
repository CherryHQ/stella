//go:build personamemeval

package reflect

import (
	"errors"
	"testing"
)

func TestBenchmarkFactReviewRetrySafeMarkerPreservesCause(t *testing.T) {
	cause := errors.New("transient provider failure")
	marked := markBenchmarkFactReviewRetrySafe(cause)

	if !BenchmarkFactReviewRetrySafe(marked) {
		t.Fatal("benchmark pre-write error was not classified as retry-safe")
	}
	if !errors.Is(marked, cause) {
		t.Fatal("benchmark retry marker does not preserve its cause")
	}
	if BenchmarkFactReviewRetrySafe(cause) {
		t.Fatal("unmarked error was classified as retry-safe")
	}
}
