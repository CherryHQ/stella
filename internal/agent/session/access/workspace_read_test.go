package access

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWorkspaceReadHasBoundedDefault(t *testing.T) {
	if workspaceReadMaxBytes != 32<<20 {
		t.Fatalf("workspaceReadMaxBytes = %d", workspaceReadMaxBytes)
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
