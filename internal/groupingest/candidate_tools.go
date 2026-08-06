package groupingest

import "github.com/CherryHQ/stella/pkg/ai"

const (
	toolSubmitGroupFactGeneration  = "submit_group_fact_generation"
	toolSubmitGroupFactEvaluations = "submit_group_fact_evaluations"
	toolSubmitGroupReconciliation  = "submit_group_fact_reconciliation"
)

func groupFactGenerationTools(candidateCap int) []ai.ToolDefinition {
	candidatesSchema := groupArraySchema(groupFactCandidateSchema())
	// Keep provider-side structured output and host validation on one cap.
	candidatesSchema["maxItems"] = candidateCap
	return []ai.ToolDefinition{{
		Name:        toolSubmitGroupFactGeneration,
		Description: "Submit all durable Group Fact candidates after reading the complete review window.",
		InputSchema: groupObjectSchema(
			[]string{"candidates"},
			map[string]any{
				"candidates": candidatesSchema,
				"no_candidate_reason": map[string]any{
					"type": "string",
				},
			},
		),
	}}
}

func groupFactEvaluationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{{
		Name:        toolSubmitGroupFactEvaluations,
		Description: "Submit one independent five-dimension evaluation for every candidate.",
		InputSchema: groupObjectSchema(
			[]string{"evaluations"},
			map[string]any{
				"evaluations": groupArraySchema(groupFactEvaluationSchema()),
			},
		),
	}}
}

func groupFactReconciliationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{{
		Name:        toolSubmitGroupReconciliation,
		Description: "Submit the complete Group Fact reconciliation plan.",
		InputSchema: groupObjectSchema(
			[]string{"operations"},
			map[string]any{
				"operations": groupArraySchema(groupObjectSchema(
					[]string{"operation", "candidate_refs", "target_fact_ids", "rationale"},
					map[string]any{
						"operation": map[string]any{
							"type": "string",
							"enum": []string{"noop", "create", "replace_many", "deprecate_many"},
						},
						"candidate_refs":  groupArraySchema(map[string]any{"type": "string"}),
						"target_fact_ids": groupArraySchema(map[string]any{"type": "string"}),
						"new_content":     map[string]any{"type": "string"},
						"rationale":       map[string]any{"type": "string"},
					},
				)),
			},
		),
	}}
}

func groupFactCandidateSchema() map[string]any {
	return groupObjectSchema(
		[]string{"subject", "content", "evidence", "expected_effect"},
		map[string]any{
			"subject": map[string]any{
				"type": "string",
				"enum": []string{"group", "human", "agent"},
			},
			"subject_ref": map[string]any{"type": "string"},
			"content":     map[string]any{"type": "string"},
			"evidence": groupArraySchema(groupObjectSchema(
				[]string{"source", "reason"},
				map[string]any{
					"source": map[string]any{"type": "string"},
					"reason": map[string]any{"type": "string"},
				},
			)),
			"expected_effect": map[string]any{"type": "string"},
		},
	)
}

func groupFactEvaluationSchema() map[string]any {
	scoreProperties := make(map[string]any, len(groupScoreFields))
	for _, field := range groupScoreFields {
		scoreProperties[field] = map[string]any{
			"type":    "integer",
			"minimum": 0,
			"maximum": 4,
		}
	}
	return groupObjectSchema(
		[]string{"candidate_ref", "scores", "rationale"},
		map[string]any{
			"candidate_ref": map[string]any{"type": "string"},
			"scores":        groupObjectSchema(groupScoreFields, scoreProperties),
			"rationale":     map[string]any{"type": "string"},
		},
	)
}

func groupObjectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           properties,
	}
}

func groupArraySchema(items map[string]any) map[string]any {
	return map[string]any{
		"type":  "array",
		"items": items,
	}
}
