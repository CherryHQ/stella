package access

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/home"
)

func TestWorkspaceReadHasBoundedDefault(t *testing.T) {
	if workspaceReadMaxBytes != 32<<20 {
		t.Fatalf("workspaceReadMaxBytes = %d", workspaceReadMaxBytes)
	}
}

func TestSuccessfulMutationCloseFailureHasUnknownOutcome(t *testing.T) {
	closeErr := errors.New("close failed")
	if err := classifyWorkspaceRootClose(nil, closeErr, home.RootReadWrite); !errors.Is(err, home.ErrOutcomeUnknown) || !errors.Is(err, closeErr) {
		t.Fatalf("RW close error = %v", err)
	}
	if err := classifyWorkspaceRootClose(nil, closeErr, home.RootReadOnly); errors.Is(err, home.ErrOutcomeUnknown) || !errors.Is(err, closeErr) {
		t.Fatalf("RO close error = %v", err)
	}
}

func TestWorkspaceUploadReaderRejectsBytesBeyondLimit(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		wantErr     error
	}{
		{name: "exact", input: "four"},
		{name: "over", input: "fives", wantErr: ErrTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := io.ReadAll(&workspaceUploadReader{reader: strings.NewReader(tt.input), remaining: 4})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ReadAll error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && string(data) != tt.input {
				t.Fatalf("data = %q, want %q", data, tt.input)
			}
		})
	}
}
