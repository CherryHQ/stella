package groupingest

import "github.com/CherryHQ/stella/pkg/ai"

const (
	toolSubmitGroupFactGeneration  = "submit_group_fact_generation"
	toolSubmitGroupFactEvaluations = "submit_group_fact_evaluations"
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
