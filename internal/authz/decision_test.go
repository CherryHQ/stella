package authz_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestResourceValidation proves an unknown resource type fails closed and a
// catalog type builds.
func TestResourceValidation(t *testing.T) {
	if _, err := authz.NewResource(authz.ResourceInvalid, "id", "owner"); err == nil {
		t.Error("invalid resource type must be rejected")
	}
	r, err := authz.NewResource(authz.ResourceSession, "s1", "owner")
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}
	if r.Type() != authz.ResourceSession || r.ID() != "s1" || r.OwnerID() != "owner" {
		t.Fatalf("resource fields wrong: %+v", r)
	}
	// A collection-level (list) resource with no id is valid.
	if _, err := authz.NewResource(authz.ResourceGoal, "", ""); err != nil {
		t.Fatalf("collection resource must build: %v", err)
	}
}

// TestRequestValidation proves an invalid action or resource fails closed.
func TestRequestValidation(t *testing.T) {
	res, err := authz.NewResource(authz.ResourceGoal, "g1", "owner")
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}
	if _, err := authz.NewRequest(authz.ActionInvalid, res, authz.InvocationFacts{}); err == nil {
		t.Error("invalid action must be rejected")
	}
	if _, err := authz.NewRequest(authz.ActionRead, authz.Resource{}, authz.InvocationFacts{}); err == nil {
		t.Error("invalid resource must be rejected")
	}
	req, err := authz.NewRequest(authz.ActionExecute, res, authz.InvocationFacts{})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.Action() != authz.ActionExecute || req.Resource().Type() != authz.ResourceGoal {
		t.Fatalf("request fields wrong: %+v", req)
	}
}

// TestTypedInvocationFacts proves facts are typed and carried immutably, and the
// group speaker is attribution only (a fact, not identity).
func TestTypedInvocationFacts(t *testing.T) {
	binding, ok := authz.NewChannelBinding("chan-1", "agent-1")
	if !ok {
		t.Fatal("NewChannelBinding must succeed for a full pair")
	}
	if _, ok := authz.NewChannelBinding("", "agent-1"); ok {
		t.Error("partial channel binding must be unset")
	}

	facts := authz.NewFactsBuilder().
		WithChannelBinding(binding).
		WithGroupSpeaker("member-42").
		Build()

	if got := facts.ChannelBinding(); got.ChannelID() != "chan-1" || got.AgentID() != "agent-1" {
		t.Fatalf("channel binding fact wrong: %+v", got)
	}
	if facts.GroupSpeaker() != "member-42" {
		t.Fatalf("group speaker fact wrong: %q", facts.GroupSpeaker())
	}
	// Empty facts are valid.
	if authz.NewFactsBuilder().Build().ChannelBinding().Set() {
		t.Error("empty facts must carry no binding")
	}
}

// TestDecisionAllowDeny proves allow/deny carry the right fields and a malformed
// deny visibility is coerced to Hidden (fail closed: reveal nothing).
func TestDecisionAllowDeny(t *testing.T) {
	audit := authz.AuditRecord{ActorKind: authz.ActorUser, Action: authz.ActionRead, Resource: authz.ResourceSession, Allowed: true, PolicyID: "p1"}
	allow := authz.Allow("p1", audit)
	if !allow.Allowed() || allow.PolicyID() != "p1" || allow.Audit().PolicyID != "p1" {
		t.Fatalf("allow decision wrong: %+v", allow)
	}

	deny := authz.Deny(authz.VisibilityForbidden, "p2", authz.AuditRecord{})
	if deny.Allowed() || deny.Visibility() != authz.VisibilityForbidden {
		t.Fatalf("deny decision wrong: %+v", deny)
	}

	// Invalid visibility coerces to Hidden.
	coerced := authz.Deny(authz.VisibilityInvalid, "p3", authz.AuditRecord{})
	if coerced.Allowed() || coerced.Visibility() != authz.VisibilityHidden {
		t.Fatalf("malformed deny must coerce to Hidden: %+v", coerced)
	}
}
