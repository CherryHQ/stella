package tasks

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// goal_context.go builds the execution context packet a plan-backed task's worker
// turn needs (#525 Phase 4): the goal it serves, the accepted plan's shape, the
// task's own slice, and the handoff summaries of its direct upstream tasks. It
// reads the plan's *accepted* content (content_json), never the in-flight
// pending edit, so a running task's prompt is stable against a concurrent replan
// (BLOCKER 1).

// GoalContextPacket is the compact working context injected into a plan-backed
// task's prompt. It is nil for standalone tasks, which keep their original
// title/description-only prompt.
type GoalContextPacket struct {
	GoalTitle       string
	GoalDescription string
	CurrentItemID   string
	Items           []PacketItem
	Upstream        []PacketHandoff
}

// PacketItem is one plan item rendered into the packet's plan outline.
type PacketItem struct {
	ID    string
	Title string
	Role  string
}

// PacketHandoff is a direct upstream task's handoff summary, the working context
// a downstream item builds on.
type PacketHandoff struct {
	Title   string
	Summary string
}

// GoalContextPacketBuilder assembles a GoalContextPacket from durable state.
type GoalContextPacketBuilder struct {
	q *sqlc.Queries
}

// NewGoalContextPacketBuilder wires a builder over the given queries.
func NewGoalContextPacketBuilder(q *sqlc.Queries) *GoalContextPacketBuilder {
	return &GoalContextPacketBuilder{q: q}
}

// Build returns the packet for a plan-backed task, or nil for a standalone task
// (no source plan / no goal). A missing goal or plan is treated as "no packet"
// rather than an error: context is advisory, and a worker must still run.
func (b *GoalContextPacketBuilder) Build(ctx context.Context, taskID string) (*GoalContextPacket, error) {
	task, err := b.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !task.SourcePlanID.Valid || !task.GoalID.Valid {
		return nil, nil
	}
	// A plan-backed task's goal and plan always exist (FK); a lookup or parse
	// failure here is a real error, which the caller degrades to "no packet"
	// (context is advisory — the worker still runs).
	goal, err := b.q.GetAgentGoal(ctx, task.GoalID.String)
	if err != nil {
		return nil, err
	}
	plan, err := b.q.GetAgentGoalPlan(ctx, task.SourcePlanID.String)
	if err != nil {
		return nil, err
	}
	// Accepted content only — a pending edit under review must not leak into a
	// running task's prompt (BLOCKER 1).
	content, err := parsePlanContent(plan.ContentJson)
	if err != nil {
		return nil, err
	}

	packet := &GoalContextPacket{
		GoalTitle:       goal.Title,
		GoalDescription: goal.Description,
		CurrentItemID:   task.PlanItemID,
	}
	for _, it := range content.Items {
		packet.Items = append(packet.Items, PacketItem{ID: it.ID, Title: it.Title, Role: it.Role})
	}

	deps, err := b.q.ListAgentTaskDeps(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		// Only hard deps gate this task and feed its context; soft deps are
		// advisory ordering and stay out of the execution packet.
		if d.DepKind != DepKindHard {
			continue
		}
		up, err := b.q.GetAgentTask(ctx, d.DepTaskID)
		if err != nil {
			continue
		}
		summary := handoffSummary(up.Output)
		if summary == "" {
			continue
		}
		packet.Upstream = append(packet.Upstream, PacketHandoff{Title: up.Title, Summary: summary})
	}
	return packet, nil
}

// Render produces the compact packet block injected into the worker prompt. It
// closes with an advisory that next_recommendations are hints, not a license to
// create tasks — work tasks come only from the materializer (the #525 gate).
func (p *GoalContextPacket) Render() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Goal context:\n")
	b.WriteString("Goal: " + p.GoalTitle + "\n")
	if p.GoalDescription != "" {
		b.WriteString(p.GoalDescription + "\n")
	}
	if len(p.Items) > 0 {
		b.WriteString("\nPlan (accepted):\n")
		for _, it := range p.Items {
			marker := "-"
			if it.ID == p.CurrentItemID {
				marker = "*" // this task's slice
			}
			line := marker + " " + it.Title
			if it.Role != "" {
				line += " [" + it.Role + "]"
			}
			b.WriteString(line + "\n")
		}
	}
	if len(p.Upstream) > 0 {
		b.WriteString("\nUpstream handoffs:\n")
		for _, h := range p.Upstream {
			b.WriteString("- " + h.Title + ": " + h.Summary + "\n")
		}
	}
	b.WriteString("\nWhen you submit, include handoff.summary describing what you produced for downstream items. next_recommendations are advisory; do not create tasks.")
	return b.String()
}

// handoffSummary extracts handoff.summary from a task's output JSON, or "" when
// absent/malformed. The handoff convention: a task's output object carries a
// `handoff` object with a `summary` string (and optional `next_recommendations`).
func handoffSummary(outputJSON string) string {
	if strings.TrimSpace(outputJSON) == "" {
		return ""
	}
	var o struct {
		Handoff struct {
			Summary string `json:"summary"`
		} `json:"handoff"`
	}
	if err := json.Unmarshal([]byte(outputJSON), &o); err != nil {
		return ""
	}
	return strings.TrimSpace(o.Handoff.Summary)
}

// requireHandoff enforces the handoff convention for a plan-backed submit.
func requireHandoff(outputJSON string) error {
	if handoffSummary(outputJSON) == "" {
		return ErrInvalidHandoff
	}
	return nil
}
