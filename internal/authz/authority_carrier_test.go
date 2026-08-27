package authz

import (
	"context"
	"testing"
)

func TestAuthorityCarrierClearsInheritedTurn(t *testing.T) {
	a, err := NewUserAuthority("user-1", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthority(context.Background(), a)
	if got, ok := AuthorityFromContext(ctx); !ok || got != a {
		t.Fatalf("carrier = %#v, %v", got, ok)
	}
	cleared := ClearAuthority(ctx)
	if _, ok := AuthorityFromContext(cleared); ok {
		t.Fatal("ClearAuthority must mask the inherited carrier")
	}
	b, err := NewUserAuthority("user-2", false)
	if err != nil {
		t.Fatal(err)
	}
	next := WithAuthority(cleared, b)
	if got, ok := AuthorityFromContext(next); !ok || got != b {
		t.Fatalf("replacement carrier = %#v, %v", got, ok)
	}
}
