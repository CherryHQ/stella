package bridge

import (
	"errors"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestMapFileCallErrorMapsStructuredTooLargeResponse(t *testing.T) {
	original := errors.New("bridge: read_file failed")
	err := mapFileCallError(response{
		Code:  codeTooLarge,
		Size:  51_066_691,
		Limit: 33_554_432,
	}, request{Op: "read_file", Path: "/app/input.csv"}, original)

	var tooLarge *sandboxpkg.FileTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v, want FileTooLargeError", err)
	}
	if tooLarge.Size != 51_066_691 || tooLarge.Limit != 33_554_432 {
		t.Fatalf("FileTooLargeError = %+v", tooLarge)
	}
	if got := err.Error(); got == "" || strings.Contains(got, "/app/input.csv") {
		t.Fatalf("generic error leaked bridge path: %q", got)
	}
}

func TestMapFileCallErrorLeavesUnstructuredTooLargeResponseGeneric(t *testing.T) {
	original := errors.New("bridge: project: request exceeds cap")
	err := mapFileCallError(response{Code: codeTooLarge}, request{Op: "project"}, original)
	if !errors.Is(err, original) {
		t.Fatalf("error = %v, want original generic transport error", err)
	}
}
