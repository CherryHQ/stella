package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
)

// ErrAccessDenied is returned by Must when the request is denied.
var ErrAccessDenied = errors.New("access denied")

// PolicyEngine evaluates access requests against loaded policies using
// a deny-overrides algorithm. Policies are loaded once at startup.
type PolicyEngine struct{ policies []Policy }

// NewEngine creates a PolicyEngine by loading all enabled policies from
// the store. Policies are sorted by priority (descending) for deterministic
// evaluation.
func NewEngine(ctx context.Context, store AuthStore) (*PolicyEngine, error) {
	policies, err := store.ListEnabledPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth engine: load policies: %w", err)
	}

	sort.Slice(policies, func(i, j int) bool {
		if policies[i].Priority != policies[j].Priority {
			return policies[i].Priority > policies[j].Priority
		}
		return policies[i].ID < policies[j].ID
	})

	return &PolicyEngine{policies: policies}, nil
}

// NewEngineFromPolicies creates a PolicyEngine from a pre-loaded set of
// policies. Useful for testing.
func NewEngineFromPolicies(policies []Policy) *PolicyEngine {
	sorted := make([]Policy, len(policies))
	copy(sorted, policies)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	return &PolicyEngine{policies: sorted}
}

// Can returns true if the access request is allowed by the loaded policies.
// It uses the deny-overrides algorithm:
//  1. Find all policies matching subject roles, action, and resource type
//  2. Evaluate conditions (ABAC) for matching policies
//  3. If ANY matching policy has effect=deny -> deny
//  4. If at least one matching policy has effect=allow -> allow
//  5. No match -> deny (default deny)
func (e *PolicyEngine) Can(ctx context.Context, req AccessRequest) bool {
	return e.evaluate(req)
}

// evaluate runs the deny-overrides algorithm and returns the authoritative
// legacy decision. It is the only source of truth for access; the shadow seam
// observes its result but never alters it.
func (e *PolicyEngine) evaluate(req AccessRequest) bool {
	var hasAllow bool

	for _, p := range e.policies {
		if !e.matchesPolicy(p, req) {
			continue
		}

		if !evaluateConditions(p.Conditions, req) {
			continue
		}

		if p.Effect == EffectDeny {
			slog.Debug("policy engine: deny",
				"policy", p.ID, "subject", req.Subject.UserID,
				"action", req.Action, "resource", req.Resource.Type)
			return false
		}

		if p.Effect == EffectAllow {
			hasAllow = true
		}
	}

	if hasAllow {
		return true
	}

	slog.Debug("policy engine: default deny (no matching allow)",
		"subject", req.Subject.UserID,
		"action", req.Action, "resource", req.Resource.Type)
	return false
}

// Must is like Can but returns ErrAccessDenied when the request is denied.
func (e *PolicyEngine) Must(ctx context.Context, req AccessRequest) error {
	if e.Can(ctx, req) {
		return nil
	}
	return ErrAccessDenied
}

// matchesPolicy checks whether a policy's subjects, actions, and resources
// match the given request (before condition evaluation).
func (e *PolicyEngine) matchesPolicy(p Policy, req AccessRequest) bool {
	return matchSubjects(p.Subjects, req.Subject) &&
		matchActions(p.Actions, req.Action) &&
		matchResources(p.Resources, req.Resource)
}

// policySubjects is the JSON structure of a policy's subjects field.
type policySubjects struct {
	Roles []string `json:"roles"`
}

// matchSubjects checks if the policy's subject roles overlap with the
// request subject's roles. Wildcard "*" matches any role.
func matchSubjects(subjectsJSON string, subject Subject) bool {
	if subjectsJSON == "" || subjectsJSON == "{}" {
		return true
	}

	var ps policySubjects
	if err := json.Unmarshal([]byte(subjectsJSON), &ps); err != nil {
		slog.Warn("policy engine: invalid subjects JSON", "json", subjectsJSON, "error", err)
		return false
	}

	if len(ps.Roles) == 0 {
		return true
	}

	for _, pr := range ps.Roles {
		if pr == "*" || slices.Contains(subject.Roles, pr) {
			return true
		}
	}

	return false
}

// matchActions checks if the policy's actions contain the request action.
// Wildcard "*" matches any action.
func matchActions(actionsJSON string, action Action) bool {
	if actionsJSON == "" || actionsJSON == "[]" {
		return true
	}

	var actions []string
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		slog.Warn("policy engine: invalid actions JSON", "json", actionsJSON, "error", err)
		return false
	}

	if len(actions) == 0 {
		return true
	}

	actionStr := string(action)
	for _, a := range actions {
		if a == "*" || a == actionStr {
			return true
		}
	}

	return false
}

// matchResources checks if the policy's resources contain the request
// resource type. Wildcard "*" matches any resource type.
func matchResources(resourcesJSON string, resource Resource) bool {
	if resourcesJSON == "" || resourcesJSON == "[]" {
		return true
	}

	var resources []string
	if err := json.Unmarshal([]byte(resourcesJSON), &resources); err != nil {
		slog.Warn("policy engine: invalid resources JSON", "json", resourcesJSON, "error", err)
		return false
	}

	if len(resources) == 0 {
		return true
	}

	resType := string(resource.Type)
	for _, r := range resources {
		if r == "*" || r == resType {
			return true
		}
	}

	return false
}
