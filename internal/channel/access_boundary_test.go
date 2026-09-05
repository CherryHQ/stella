package channel

import (
	"errors"
	"fmt"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestMapPublicAccessErrorPreservesForbiddenCategoryAndCause(t *testing.T) {
	original := fmt.Errorf("attachment denied: %w", agentaccess.ErrForbidden)
	mapped := mapPublicAccessError(original)

	if mapped.Error() != original.Error() {
		t.Fatalf("error text = %q, want %q", mapped, original)
	}
	if !errors.Is(mapped, agentaccess.ErrForbidden) {
		t.Fatal("mapped error lost the internal forbidden cause")
	}
	if !errors.Is(mapped, pkgchannel.ErrAgentAccessForbidden) {
		t.Fatal("mapped error is not classified as public forbidden")
	}
	if errors.Is(mapped, pkgchannel.ErrAgentAccessDenied) {
		t.Fatal("forbidden error was broadened to the channel denied category")
	}
}

func TestMapPublicAccessErrorPreservesDeniedCategoryAndCause(t *testing.T) {
	original := fmt.Errorf("attachment denied: %w", ErrAgentAccessDenied)
	mapped := mapPublicAccessError(original)

	if mapped.Error() != original.Error() {
		t.Fatalf("error text = %q, want %q", mapped, original)
	}
	if !errors.Is(mapped, ErrAgentAccessDenied) {
		t.Fatal("mapped error lost the internal denied cause")
	}
	if !errors.Is(mapped, pkgchannel.ErrAgentAccessDenied) {
		t.Fatal("mapped error is not classified as public denied")
	}
	if errors.Is(mapped, pkgchannel.ErrAgentAccessForbidden) {
		t.Fatal("denied error was broadened to the public forbidden category")
	}
}
