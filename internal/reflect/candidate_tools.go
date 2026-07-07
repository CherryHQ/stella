package reflect

import "github.com/CherryHQ/stella/pkg/ai"

func factGenerationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{
		captureTool(toolSubmitFactGeneration, "Submit all durable fact candidates for this review.", objectSchema(
			requiredProps("candidates"),
			prop("candidates", arraySchema(factCandidateSchema())),
			prop("no_candidate_reason", stringSchema()),
		)),
	}
}

func factEvaluationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{
		captureTool(toolSubmitFactEvaluations, "Submit scores for all fact candidates.", objectSchema(
			requiredProps("evaluations"),
			prop("evaluations", arraySchema(factEvaluationSchema())),
		)),
	}
}

func skillGenerationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{
		captureTool(toolSubmitSkillGeneration, "Submit all reusable skill learning candidates for this review.", objectSchema(
			requiredProps("candidates"),
			prop("candidates", arraySchema(skillCandidateSchema())),
			prop("no_candidate_reason", stringSchema()),
		)),
	}
}

func skillEvaluationTools() []ai.ToolDefinition {
	return []ai.ToolDefinition{
		captureTool(toolSubmitSkillEvaluations, "Submit scores for all skill candidates.", objectSchema(
			requiredProps("evaluations"),
			prop("evaluations", arraySchema(skillEvaluationSchema())),
		)),
	}
}

func captureTool(name, description string, schema map[string]any) ai.ToolDefinition {
	return ai.ToolDefinition{Name: name, Description: description, InputSchema: schema}
}

func factCandidateSchema() map[string]any {
	return objectSchema(
		requiredProps("subject", "content", "evidence", "expected_effect", "handoff_hints"),
		prop("subject", enumSchema("user", "agent", "world")),
		prop("content", stringSchema()),
		prop("evidence", arraySchema(objectSchema(
			requiredProps("source_type", "source", "reason"),
			prop("source_type", enumSchema("user_message", "user_correction", "tool_result", "agent_soul_instruction")),
			prop("source", stringSchema()),
			prop("reason", stringSchema()),
		))),
		prop("expected_effect", stringSchema()),
		prop("handoff_hints", objectSchema(
			prop("knowledge_search_query_hint", stringSchema()),
		)),
	)
}

func factEvaluationSchema() map[string]any {
	return objectSchema(
		requiredProps("candidate_ref", "scores", "rationale"),
		prop("candidate_ref", stringSchema()),
		prop("scores", objectSchema(
			requiredProps(
				factScoreEvidenceStrength,
				factScoreSubjectFit,
				factScoreDurability,
				factScoreFutureUtility,
				factScoreAtomicity,
			),
			prop(factScoreEvidenceStrength, scoreSchema()),
			prop(factScoreSubjectFit, scoreSchema()),
			prop(factScoreDurability, scoreSchema()),
			prop(factScoreFutureUtility, scoreSchema()),
			prop(factScoreAtomicity, scoreSchema()),
		)),
		prop("rationale", stringSchema()),
	)
}

func skillCandidateSchema() map[string]any {
	return objectSchema(
		requiredProps("learning", "evidence", "applicability", "procedure", "handoff_hints"),
		prop("learning", objectSchema(
			requiredProps("summary", "reusable_delta"),
			prop("summary", stringSchema()),
			prop("reusable_delta", stringSchema()),
		)),
		prop("evidence", arraySchema(objectSchema(
			requiredProps("signal_type", "source", "reason"),
			prop("signal_type", enumSchema("user_correction", "successful_workflow", "failure_recovery", "tooling_discovery", "explicit_instruction", "skill_gap")),
			prop("source", stringSchema()),
			prop("reason", stringSchema()),
		))),
		prop("applicability", objectSchema(
			requiredProps("trigger_examples", "non_trigger_examples"),
			prop("trigger_examples", arraySchema(stringSchema())),
			prop("non_trigger_examples", arraySchema(stringSchema())),
		)),
		prop("procedure", objectSchema(
			requiredProps("steps", "verification"),
			prop("prerequisites", arraySchema(stringSchema())),
			prop("steps", arraySchema(stringSchema())),
			prop("decision_points", arraySchema(stringSchema())),
			prop("pitfalls", arraySchema(stringSchema())),
			prop("verification", arraySchema(stringSchema())),
		)),
		prop("session_skill_context", objectSchema(
			requiredProps("used_skill_refs", "change_against_loaded_skill"),
			prop("used_skill_refs", arraySchema(stringSchema())),
			prop("change_against_loaded_skill", stringSchema()),
		)),
		prop("handoff_hints", objectSchema(
			requiredProps("search_query_hint"),
			prop("search_query_hint", stringSchema()),
		)),
	)
}

func skillEvaluationSchema() map[string]any {
	return objectSchema(
		requiredProps("candidate_ref", "scores", "rationale"),
		prop("candidate_ref", stringSchema()),
		prop("scores", objectSchema(
			requiredProps(
				skillScoreEvidenceStrength,
				skillScoreReusableValue,
				skillScoreBaselineSeparation,
				skillScoreProcedureActionability,
				skillScoreApplicabilityClarity,
				skillScoreVerificationQuality,
			),
			prop(skillScoreEvidenceStrength, scoreSchema()),
			prop(skillScoreReusableValue, scoreSchema()),
			prop(skillScoreBaselineSeparation, scoreSchema()),
			prop(skillScoreProcedureActionability, scoreSchema()),
			prop(skillScoreApplicabilityClarity, scoreSchema()),
			prop(skillScoreVerificationQuality, scoreSchema()),
		)),
		prop("rationale", stringSchema()),
	)
}

type schemaOption func(map[string]any)

func objectSchema(opts ...schemaOption) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	for _, opt := range opts {
		opt(schema)
	}
	return schema
}

func requiredProps(names ...string) schemaOption {
	return func(schema map[string]any) {
		schema["required"] = names
	}
}

func prop(name string, schema map[string]any) schemaOption {
	return func(parent map[string]any) {
		props, _ := parent["properties"].(map[string]any)
		props[name] = schema
	}
}

func stringSchema() map[string]any {
	return map[string]any{"type": "string"}
}

func scoreSchema() map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "maximum": maxScoreValue}
}

func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}
