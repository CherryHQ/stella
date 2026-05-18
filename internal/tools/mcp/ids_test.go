package mcp

import "testing"

func TestSanitizeName(t *testing.T) {
	if got := SanitizeName("Stripe Dev#1"); got != "stripedev1" {
		t.Fatalf("SanitizeName = %q, want stripedev1", got)
	}
	if got := SanitizeName("---"); got != "unnamed" {
		t.Fatalf("SanitizeName empty = %q, want unnamed", got)
	}
}

func TestCanonicalRegistryAddAndResolve(t *testing.T) {
	reg := NewCanonicalRegistry()
	first := reg.Add("Stripe Dev", "Create Customer")
	second := reg.Add("Stripe+Dev", "Create Customer")
	if first != "mcp__stripedev__createcustomer" {
		t.Fatalf("first id = %q", first)
	}
	if second != "mcp__stripedev__createcustomer__2" {
		t.Fatalf("second id = %q", second)
	}
	resolved, ok := reg.Resolve(second)
	if !ok {
		t.Fatal("expected to resolve second id")
	}
	if resolved.ServerName != "Stripe+Dev" || resolved.ToolName != "Create Customer" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestParseToolID(t *testing.T) {
	target, err := ParseToolID("mcp__github__searchrepos")
	if err != nil {
		t.Fatalf("ParseToolID: %v", err)
	}
	if target.ServerName != "github" || target.ToolName != "searchrepos" {
		t.Fatalf("target = %+v", target)
	}
}
