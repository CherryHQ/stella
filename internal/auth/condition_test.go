package auth

import "testing"

func TestEvaluateConditions_Empty(t *testing.T) {
	req := AccessRequest{}
	for _, cond := range []string{"", "{}", `{}`} {
		if !evaluateConditions(cond, req) {
			t.Errorf("expected empty conditions %q to match", cond)
		}
	}
}

func TestEvaluateConditions_InvalidJSON(t *testing.T) {
	req := AccessRequest{}
	if evaluateConditions("not-json", req) {
		t.Error("expected invalid JSON conditions to fail")
	}
}

func TestEvaluateConditions_EqLiteral(t *testing.T) {
	req := AccessRequest{
		Resource: Resource{
			Attrs: map[string]any{"scope": "system"},
		},
	}
	cond := `{"resource.scope":{"eq":"system"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected eq literal to match")
	}

	cond2 := `{"resource.scope":{"eq":"private"}}`
	if evaluateConditions(cond2, req) {
		t.Error("expected eq literal mismatch to fail")
	}
}

func TestEvaluateConditions_EqAttrRef(t *testing.T) {
	req := AccessRequest{
		Subject:  Subject{UserID: "42"},
		Resource: Resource{OwnerID: "42"},
	}
	cond := `{"resource.owner_id":{"eq":"subject.id"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected owner_id == subject.id to match")
	}

	req.Resource.OwnerID = "99"
	if evaluateConditions(cond, req) {
		t.Error("expected owner_id != subject.id to fail")
	}
}

func TestEvaluateConditions_Neq(t *testing.T) {
	req := AccessRequest{
		Resource: Resource{
			Attrs: map[string]any{"status": "active"},
		},
	}
	cond := `{"resource.status":{"neq":"disabled"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected neq to match")
	}

	cond2 := `{"resource.status":{"neq":"active"}}`
	if evaluateConditions(cond2, req) {
		t.Error("expected neq same value to fail")
	}
}

func TestEvaluateConditions_In(t *testing.T) {
	req := AccessRequest{
		Subject: Subject{
			UserID:   "1",
			AgentIDs: []string{"agent-a", "agent-b", "agent-c"},
		},
		Resource: Resource{ID: "agent-b"},
	}
	cond := `{"resource.id":{"in":"subject.agent_ids"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected 'in' to match")
	}

	req.Resource.ID = "agent-z"
	if evaluateConditions(cond, req) {
		t.Error("expected 'in' to fail for non-member")
	}
}

func TestEvaluateConditions_InLiteralArray(t *testing.T) {
	req := AccessRequest{
		Resource: Resource{
			Attrs: map[string]any{"scope": "system"},
		},
	}
	// Right operand is a literal JSON array embedded as the value.
	cond := `{"resource.scope":{"in":["system","public"]}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected 'in' literal array to match")
	}

	req.Resource.Attrs["scope"] = "private"
	if evaluateConditions(cond, req) {
		t.Error("expected 'in' literal array to fail for non-member")
	}
}

func TestEvaluateConditions_NotIn(t *testing.T) {
	req := AccessRequest{
		Subject: Subject{
			AgentIDs: []string{"a", "b"},
		},
		Resource: Resource{ID: "c"},
	}
	cond := `{"resource.id":{"not_in":"subject.agent_ids"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected not_in to match when not a member")
	}

	req.Resource.ID = "a"
	if evaluateConditions(cond, req) {
		t.Error("expected not_in to fail when is a member")
	}
}

func TestEvaluateConditions_Contains(t *testing.T) {
	req := AccessRequest{
		Subject: Subject{
			Roles: []string{"admin", "user"},
		},
	}
	// "subject.roles" contains "admin"
	cond := `{"subject.roles":{"contains":"admin"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected contains to match")
	}

	cond2 := `{"subject.roles":{"contains":"superadmin"}}`
	if evaluateConditions(cond2, req) {
		t.Error("expected contains to fail for non-member")
	}
}

func TestEvaluateConditions_MultipleConditionsAND(t *testing.T) {
	req := AccessRequest{
		Subject: Subject{UserID: "5"},
		Resource: Resource{
			OwnerID: "5",
			Attrs:   map[string]any{"scope": "system"},
		},
	}
	cond := `{"resource.owner_id":{"eq":"subject.id"},"resource.scope":{"eq":"system"}}`
	if !evaluateConditions(cond, req) {
		t.Error("expected both conditions to match")
	}

	// Change scope so second condition fails.
	req.Resource.Attrs["scope"] = "private"
	if evaluateConditions(cond, req) {
		t.Error("expected AND to fail when one condition fails")
	}
}

func TestEvaluateConditions_UnknownOperator(t *testing.T) {
	req := AccessRequest{
		Resource: Resource{Attrs: map[string]any{"x": "1"}},
	}
	cond := `{"resource.x":{"gt":"0"}}`
	if evaluateConditions(cond, req) {
		t.Error("expected unknown operator to fail")
	}
}

func TestResolveAttribute_InvalidPath(t *testing.T) {
	req := AccessRequest{}
	if v := resolveAttribute("noprefix", req); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
	if v := resolveAttribute("unknown.field", req); v != "" {
		t.Errorf("expected empty for unknown prefix, got %q", v)
	}
}

func TestResolveSubjectAttr_CustomAttrs(t *testing.T) {
	s := Subject{Attrs: map[string]any{"department": "engineering"}}
	if v := resolveSubjectAttr("department", s); v != "engineering" {
		t.Errorf("expected 'engineering', got %q", v)
	}
}

func TestResolveResourceAttr_CustomAttrs(t *testing.T) {
	r := Resource{Attrs: map[string]any{"scope": "system"}}
	if v := resolveResourceAttr("scope", r); v != "system" {
		t.Errorf("expected 'system', got %q", v)
	}
}

func TestResolveResourceAttr_NilAttrs(t *testing.T) {
	r := Resource{}
	if v := resolveResourceAttr("unknown", r); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
}

func TestAnyToString(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{nil, ""},
		{"hello", "hello"},
		{float64(42), "42"},
		{float64(3.14), "3.14"},
		{int64(10), "10"},
		{int(7), "7"},
		{true, "true"},
		{false, "false"},
	}
	for _, tt := range tests {
		got := anyToString(tt.input)
		if got != tt.want {
			t.Errorf("anyToString(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
