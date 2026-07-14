package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestBeginUsesStaticRulesWithoutDatabase(t *testing.T) {
	az := New()
	user := userAuthority(t, "u1", false)

	first, err := az.Begin(context.Background(), user)
	if err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	second, err := az.Begin(context.Background(), user)
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	if first.Revision() != staticRevision || second.Revision() != staticRevision {
		t.Fatalf("static revisions = %d, %d; want %d", first.Revision(), second.Revision(), staticRevision)
	}
	if decision, err := first.Decide(mustAgentRead(t, "system", "system", "system", false)); err != nil || !decision.Allowed() {
		t.Fatalf("static system-agent decision = %+v, %v; want allow", decision, err)
	}
}

func TestBeginRejectsInvalidAuthority(t *testing.T) {
	_, err := New().Begin(context.Background(), authz.Authority{})
	if !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("Begin with zero authority = %v, want ErrInvalidAuthority", err)
	}
}
