package tasks

import (
	"encoding/json"
	"fmt"
)

// Plan statuses (agent_goal_plan.status). Enforced in Go, not a schema CHECK.
// A plan is edited in place: draft -> (in_review -> approved | accepted) and
// back to draft on changes-requested. "Materialized" is not a status; it is the
// materialized_at timestamp set inside MaterializeGoalPlan's tx.
const (
	PlanStatusDraft    = "draft"
	PlanStatusInReview = "in_review"
	PlanStatusApproved = "approved"
	PlanStatusAccepted = "accepted"
)

// Plan modes select how CreateGoal seeds a goal's plan. "direct" auto-creates,
// accepts, and materializes a one-task direct plan so the goal lands in
// 'planned' ready to activate; "deferred" leaves the goal in 'draft' with no
// plan row, so a later CreateGoalPlan is the first and only insert.
const (
	PlanModeDirect   = "direct"
	PlanModeDeferred = "deferred"
)

// Plan item roles (a content-level label inside content_json, not a DB column).
// "direct" tags the single item of a direct plan; design/impl/verify shape a
// structured plan. Ordering comes entirely from item deps, never from the role.
const (
	PlanRoleDirect = "direct"
	PlanRoleDesign = "design"
	PlanRoleImpl   = "impl"
	PlanRoleVerify = "verify"
)

// PlanItem is one node of a plan. Deps lists the upstream item IDs this item
// depends on (edge item -> dep, dep runs first). The materializer turns each
// item into an agent_task and each dep into an agent_task_dep edge.
type PlanItem struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Role  string   `json:"role,omitempty"`
	Deps  []string `json:"deps,omitempty"`
	// Criteria are the task's acceptance criteria, materialized into
	// agent_task_criterion rows (optional; order preserved as position).
	Criteria []string `json:"criteria,omitempty"`
}

// PlanContent is the parsed form of content_json / pending_content_json.
type PlanContent struct {
	Items []PlanItem `json:"items"`
}

// parsePlanContent decodes a content_json / pending_content_json string. An
// empty string is an empty (item-less) plan, which validatePlan then rejects —
// callers do not special-case it.
func parsePlanContent(raw string) (PlanContent, error) {
	var c PlanContent
	if raw == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("%w: malformed content_json: %w", ErrInvalidPlan, err)
	}
	return c, nil
}

// validatePlan is the machine-checkable plan-quality gate (plan D3). It runs
// before a plan is accepted or materialized, so a structurally bad plan never
// produces work tasks. Two modes:
//
//   - direct (exactly one item): role empty or "direct", no deps. The
//     impl->verify rule does not apply.
//   - structured (>1 item): every item carries role design|impl|verify, there
//     is at least one impl, and every impl has a downstream verify depending on
//     it (transitively).
//
// Both modes require a non-empty item set, unique non-empty ids/titles, deps
// that reference existing items, an acyclic graph, and at least one terminal
// item to roll the goal up.
func validatePlan(content PlanContent) error {
	items := content.Items
	if len(items) == 0 {
		return fmt.Errorf("%w: plan has no items", ErrInvalidPlan)
	}

	byID := make(map[string]PlanItem, len(items))
	for _, it := range items {
		if it.ID == "" {
			return fmt.Errorf("%w: item with empty id", ErrInvalidPlan)
		}
		if it.Title == "" {
			return fmt.Errorf("%w: item %q has empty title", ErrInvalidPlan, it.ID)
		}
		if _, dup := byID[it.ID]; dup {
			return fmt.Errorf("%w: duplicate item id %q", ErrInvalidPlan, it.ID)
		}
		byID[it.ID] = it
	}

	for _, it := range items {
		for _, d := range it.Deps {
			if d == it.ID {
				return fmt.Errorf("%w: item %q depends on itself", ErrInvalidPlan, it.ID)
			}
			if _, ok := byID[d]; !ok {
				return fmt.Errorf("%w: item %q depends on unknown item %q", ErrInvalidPlan, it.ID, d)
			}
		}
	}

	if err := planAcyclic(items, byID); err != nil {
		return err
	}

	if len(items) == 1 {
		it := items[0]
		if it.Role != "" && it.Role != PlanRoleDirect {
			return fmt.Errorf("%w: single-item plan must be direct (role empty or %q), got %q", ErrInvalidPlan, PlanRoleDirect, it.Role)
		}
		if len(it.Deps) != 0 {
			return fmt.Errorf("%w: single-item direct plan cannot have deps", ErrInvalidPlan)
		}
		return nil
	}

	// Structured mode.
	implCount := 0
	for _, it := range items {
		switch it.Role {
		case PlanRoleDesign, PlanRoleImpl, PlanRoleVerify:
		default:
			return fmt.Errorf("%w: structured item %q needs role design|impl|verify, got %q", ErrInvalidPlan, it.ID, it.Role)
		}
		if it.Role == PlanRoleImpl {
			implCount++
		}
	}
	if implCount == 0 {
		return fmt.Errorf("%w: structured plan needs at least one impl item", ErrInvalidPlan)
	}
	for _, it := range items {
		if it.Role != PlanRoleImpl {
			continue
		}
		if !hasDownstreamVerify(it.ID, items, byID) {
			return fmt.Errorf("%w: impl item %q has no downstream verify", ErrInvalidPlan, it.ID)
		}
	}
	if !hasTerminal(items) {
		return fmt.Errorf("%w: plan has no terminal item to roll up", ErrInvalidPlan)
	}
	return nil
}

// planAcyclic reports a dependency cycle via DFS three-coloring.
func planAcyclic(items []PlanItem, byID map[string]PlanItem) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(items))
	var visit func(id string) error
	visit = func(id string) error {
		color[id] = gray
		for _, d := range byID[id].Deps {
			switch color[d] {
			case gray:
				return fmt.Errorf("%w: dependency cycle at item %q", ErrInvalidPlan, d)
			case white:
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	for _, it := range items {
		if color[it.ID] == white {
			if err := visit(it.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// hasDownstreamVerify reports whether some verify item transitively depends on
// implID (i.e. implID is upstream of a verify).
func hasDownstreamVerify(implID string, items []PlanItem, byID map[string]PlanItem) bool {
	for _, v := range items {
		if v.Role == PlanRoleVerify && dependsOn(v.ID, implID, byID, map[string]bool{}) {
			return true
		}
	}
	return false
}

// dependsOn reports whether item a transitively depends on item b via deps.
func dependsOn(a, b string, byID map[string]PlanItem, seen map[string]bool) bool {
	if seen[a] {
		return false
	}
	seen[a] = true
	for _, d := range byID[a].Deps {
		if d == b || dependsOn(d, b, byID, seen) {
			return true
		}
	}
	return false
}

// hasTerminal reports whether at least one item is a sink (no other item
// depends on it) — the item whose completion rolls the goal up.
func hasTerminal(items []PlanItem) bool {
	depended := make(map[string]bool, len(items))
	for _, it := range items {
		for _, d := range it.Deps {
			depended[d] = true
		}
	}
	for _, it := range items {
		if !depended[it.ID] {
			return true
		}
	}
	return false
}
