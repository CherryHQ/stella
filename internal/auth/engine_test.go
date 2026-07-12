package auth

import (
	"context"
	"errors"
	"testing"
)

// --- Policy matching tests ---

func TestMatchSubjects_Wildcard(t *testing.T) {
	if !matchSubjects(`{"roles":["*"]}`, Subject{Roles: []string{"user"}}) {
		t.Error("wildcard should match any role")
	}
}

func TestMatchSubjects_SpecificRole(t *testing.T) {
	if !matchSubjects(`{"roles":["admin"]}`, Subject{Roles: []string{"admin", "user"}}) {
		t.Error("should match when subject has the role")
	}
	if matchSubjects(`{"roles":["admin"]}`, Subject{Roles: []string{"user"}}) {
		t.Error("should not match when subject lacks the role")
	}
}

func TestMatchSubjects_EmptyJSON(t *testing.T) {
	if !matchSubjects("", Subject{}) {
		t.Error("empty subjects should match")
	}
	if !matchSubjects("{}", Subject{}) {
		t.Error("empty object subjects should match")
	}
}

func TestMatchSubjects_EmptyRoles(t *testing.T) {
	if !matchSubjects(`{"roles":[]}`, Subject{Roles: []string{"user"}}) {
		t.Error("empty roles array should match any subject")
	}
}

func TestMatchSubjects_InvalidJSON(t *testing.T) {
	if matchSubjects("invalid", Subject{Roles: []string{"admin"}}) {
		t.Error("invalid JSON should not match")
	}
}

func TestMatchActions_Wildcard(t *testing.T) {
	if !matchActions(`["*"]`, ActionRead) {
		t.Error("wildcard should match any action")
	}
}

func TestMatchActions_Specific(t *testing.T) {
	if !matchActions(`["read","write"]`, ActionRead) {
		t.Error("should match listed action")
	}
	if matchActions(`["read","write"]`, ActionDelete) {
		t.Error("should not match unlisted action")
	}
}

func TestMatchActions_Empty(t *testing.T) {
	if !matchActions("", ActionRead) {
		t.Error("empty actions should match")
	}
	if !matchActions("[]", ActionRead) {
		t.Error("empty array actions should match")
	}
}

func TestMatchActions_InvalidJSON(t *testing.T) {
	if matchActions("invalid", ActionRead) {
		t.Error("invalid JSON should not match")
	}
}

func TestMatchResources_Wildcard(t *testing.T) {
	if !matchResources(`["*"]`, Resource{Type: ResourceAgent}) {
		t.Error("wildcard should match any resource")
	}
}

func TestMatchResources_Specific(t *testing.T) {
	if !matchResources(`["agent","provider"]`, Resource{Type: ResourceAgent}) {
		t.Error("should match listed resource")
	}
	if matchResources(`["agent","provider"]`, Resource{Type: ResourceSession}) {
		t.Error("should not match unlisted resource")
	}
}

func TestMatchResources_Empty(t *testing.T) {
	if !matchResources("", Resource{Type: ResourceAgent}) {
		t.Error("empty resources should match")
	}
}

func TestMatchResources_InvalidJSON(t *testing.T) {
	if matchResources("invalid", Resource{Type: ResourceAgent}) {
		t.Error("invalid JSON should not match")
	}
}

// --- Deny-overrides tests ---

func TestEngine_DenyOverrides(t *testing.T) {
	policies := []Policy{
		{
			ID:         "allow-all",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["*"]`,
			Resources:  `["*"]`,
			Conditions: `{}`,
			Priority:   10,
			Enabled:    true,
		},
		{
			ID:         "deny-delete",
			Effect:     EffectDeny,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["delete"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   20,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	// Read should be allowed.
	if !engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("read should be allowed")
	}

	// Delete agent should be denied (deny overrides allow).
	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionDelete,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("delete agent should be denied")
	}
}

func TestEngine_DefaultDeny(t *testing.T) {
	// No policies at all -> default deny.
	engine := NewEngineFromPolicies(nil)
	ctx := context.Background()

	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("should default deny with no policies")
	}
}

func TestEngine_NoMatchingPolicies(t *testing.T) {
	policies := []Policy{
		{
			ID:         "admin-only",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["admin"]}`,
			Actions:    `["*"]`,
			Resources:  `["*"]`,
			Conditions: `{}`,
			Priority:   100,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	// User (not admin) should be denied.
	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("non-admin should be denied when only admin policy exists")
	}
}

func TestEngine_AllowWhenMatched(t *testing.T) {
	policies := []Policy{
		{
			ID:         "allow-user-read",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   50,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	if !engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("should allow when policy matches")
	}
}

func TestEngine_ConditionBasedDenial(t *testing.T) {
	policies := []Policy{
		{
			ID:         "allow-own-data",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read","write"]`,
			Resources:  `["user_data"]`,
			Conditions: `{"resource.owner_id":{"eq":"subject.id"}}`,
			Priority:   50,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	// Own data: allowed.
	if !engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "1", Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceUserData, OwnerID: "1"},
	}) {
		t.Error("should allow access to own data")
	}

	// Other's data: denied (condition doesn't match -> no allow policy matches).
	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "1", Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceUserData, OwnerID: "99"},
	}) {
		t.Error("should deny access to other user's data")
	}
}

func TestEngine_PriorityOrdering(t *testing.T) {
	// Lower priority allow, higher priority deny -> deny wins.
	policies := []Policy{
		{
			ID:         "low-allow",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   10,
			Enabled:    true,
		},
		{
			ID:         "high-deny",
			Effect:     EffectDeny,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   100,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("deny should override allow regardless of priority")
	}
}

func TestEngine_Must(t *testing.T) {
	engine := NewEngineFromPolicies(nil)
	ctx := context.Background()

	err := engine.Must(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	})
	if err == nil {
		t.Error("Must should return error when denied")
	}
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("expected ErrAccessDenied, got %v", err)
	}
}

func TestEngine_MustAllowed(t *testing.T) {
	policies := []Policy{
		{
			ID:         "allow",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["*"]}`,
			Actions:    `["*"]`,
			Resources:  `["*"]`,
			Conditions: `{}`,
			Priority:   1,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	if err := engine.Must(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}); err != nil {
		t.Errorf("Must should return nil when allowed, got %v", err)
	}
}

func TestEngine_MultipleRolesOnSubject(t *testing.T) {
	policies := []Policy{
		{
			ID:         "admin-full",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["admin"]}`,
			Actions:    `["*"]`,
			Resources:  `["*"]`,
			Conditions: `{}`,
			Priority:   100,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	// Subject with both admin and user roles should match admin policy.
	if !engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user", "admin"}},
		Action:   ActionManage,
		Resource: Resource{Type: ResourceSetting},
	}) {
		t.Error("should allow when subject has matching role among multiple")
	}
}

func TestEngine_ConflictingPolicies_DenyWins(t *testing.T) {
	policies := []Policy{
		{
			ID:         "allow-read",
			Effect:     EffectAllow,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   50,
			Enabled:    true,
		},
		{
			ID:         "deny-read",
			Effect:     EffectDeny,
			Subjects:   `{"roles":["user"]}`,
			Actions:    `["read"]`,
			Resources:  `["agent"]`,
			Conditions: `{}`,
			Priority:   50,
			Enabled:    true,
		},
	}

	engine := NewEngineFromPolicies(policies)
	ctx := context.Background()

	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgent},
	}) {
		t.Error("deny should win over allow at same priority")
	}
}

// --- Built-in policy scenario tests ---

func TestEngine_BuiltinPolicies_AdminFullAccess(t *testing.T) {
	engine := NewEngineFromPolicies(builtinPolicies)
	ctx := context.Background()

	// Admin can do anything.
	for _, action := range []Action{ActionRead, ActionWrite, ActionCreate, ActionDelete, ActionManage} {
		for _, res := range []ResourceType{ResourceAgent, ResourceProvider, ResourceSession, ResourceUser} {
			if !engine.Can(ctx, AccessRequest{
				Subject:  Subject{UserID: "1", Roles: []string{"admin"}},
				Action:   action,
				Resource: Resource{Type: res},
			}) {
				t.Errorf("admin should have full access: action=%s resource=%s", action, res)
			}
		}
	}
}

func TestEngine_BuiltinPolicies_UserOwnData(t *testing.T) {
	engine := NewEngineFromPolicies(builtinPolicies)
	ctx := context.Background()

	// User can read own data.
	if !engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "5", Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceUserData, OwnerID: "5"},
	}) {
		t.Error("user should be able to read own data")
	}

	// User cannot read other's data.
	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "5", Roles: []string{"user"}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceUserData, OwnerID: "99"},
	}) {
		t.Error("user should not be able to read other's data")
	}
}

func TestEngine_BuiltinPolicies_UserCannotManageProviders(t *testing.T) {
	engine := NewEngineFromPolicies(builtinPolicies)
	ctx := context.Background()

	if engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "2", Roles: []string{"user"}},
		Action:   ActionManage,
		Resource: Resource{Type: ResourceProvider},
	}) {
		t.Error("user should not be able to manage providers")
	}
}
