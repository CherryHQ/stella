package reflect

import (
	"strings"
	"testing"
)

func TestCombinedReviewPrompt_ExplicitlyProhibitsConstraintActions(t *testing.T) {
	for _, action := range []string{"constraint_add", "constraint_remove", "constraint_list"} {
		if !strings.Contains(combinedReviewPrompt, action) {
			t.Errorf("expected reflect prompt to mention %q as off-limits", action)
		}
	}

	// The prohibition should appear in the Constraints section.
	if !strings.Contains(combinedReviewPrompt, "## Constraints") {
		t.Error("expected ## Constraints section in reflect prompt")
	}
	if !strings.Contains(combinedReviewPrompt, "off-limits") {
		t.Error("expected 'off-limits' in reflect prompt constraints section")
	}
}

func TestCombinedReviewPrompt_UsesFactSubjectRouting(t *testing.T) {
	for _, phrase := range []string{
		"subject=user",
		"subject=agent",
		"subject=world",
		"Do NOT use the skills tool for knowledge facts",
	} {
		if !strings.Contains(combinedReviewPrompt, phrase) {
			t.Errorf("expected reflect prompt to contain %q", phrase)
		}
	}
	if strings.Contains(combinedReviewPrompt, `knowledge_type="fact"`) {
		t.Error("reflect prompt should not route knowledge facts through skill metadata")
	}
}

func TestCombinedReviewPrompt_DefersWorldFactWrites(t *testing.T) {
	for _, phrase := range []string{
		"Identify subject=world candidates",
		"Do NOT create or write subject=world facts",
	} {
		if !strings.Contains(combinedReviewPrompt, phrase) {
			t.Errorf("expected reflect prompt to contain %q", phrase)
		}
	}
	if strings.Contains(combinedReviewPrompt, "Create a subject=world fact") {
		t.Error("reflect prompt should not claim this review can create subject=world facts")
	}
}

func TestFactCandidatePrompts_DefineUserFactDurabilityBoundaries(t *testing.T) {
	for _, phrase := range []string{
		"Do not require the user to ask to remember it",
		"stable user preferences",
		"professional or domain context",
		"tools, software, equipment, or ecosystems the user uses",
		"ongoing learning goals",
		"recurring habits or activities",
		"time-bound plans",
		"current progress",
		"in-progress decision states",
		"active high-stakes personal matters",
		"temporary or low-future-value situational detail",
		"non-user-subject detail",
		"recurring learning or practice preference",
	} {
		if !strings.Contains(factCandidateGenerationPrompt, phrase) {
			t.Errorf("expected fact generation prompt to contain %q", phrase)
		}
	}

	for _, phrase := range []string{
		"Score durability 0 or 1",
		"time-bound plan",
		"current progress",
		"in-progress decision state",
		"active high-stakes personal matter",
		"Stable user preferences",
		"professional or domain context",
		"ongoing learning goals",
		"low-future-value situational detail",
		"non-user-subject detail",
		"recurring learning or practice preference",
	} {
		if !strings.Contains(factCandidateEvaluationPrompt, phrase) {
			t.Errorf("expected fact evaluation prompt to contain %q", phrase)
		}
	}
}

func TestFactCandidatePrompts_RouteProjectKnowledgeToWorld(t *testing.T) {
	assertPromptContainsAll(t, factCandidateGenerationPrompt, []string{
		"subject=world is for project, repository, environment, tooling, workflow, domain, or external-world knowledge",
		"Do not route project, repository, environment, tooling, workflow, or domain knowledge as subject=user",
		"Use subject=user only for facts about the user's own profile",
	})

	assertPromptContainsAll(t, factCandidateEvaluationPrompt, []string{
		"Project, repository, environment, tooling, workflow, domain, or external-world knowledge belongs in subject=world",
		"score subject_fit below 2 if project or environment knowledge is routed as subject=user",
	})
}

func TestFactCandidateGenerationPrompt_RequiresAtomicFactCandidates(t *testing.T) {
	assertPromptContainsAll(t, factCandidateGenerationPrompt, []string{
		"Each fact candidate must be atomic",
		"Atomicity applies to subject=user, subject=agent, and subject=world",
		"split them into separate candidates",
		"Do not bundle ownership/count, use cases, mileage or progress, goals, preferences, and routines into one candidate",
		"Do not bundle independent project knowledge facts, such as source-of-truth rules, generated-file rules, and generated-route handling rules",
		"Prefer fewer, precise candidates over one broad mixed candidate",
	})
}

func TestFactCandidateEvaluationPrompt_UsesDetailedScoreRubric(t *testing.T) {
	assertPromptContainsAll(t, factCandidateEvaluationPrompt, []string{
		"Do not collapse 2 and 3",
		"evidence_strength=0: no evidence, or evidence comes from system prompts",
		"evidence_strength=1: evidence is vague or mostly inferred",
		"evidence_strength=2: fresh evidence supports part of the content",
		"evidence_strength=3: fresh evidence directly supports the main content",
		"evidence_strength=4: multiple clear fresh evidence items support both content and route",
		"subject_fit=0: subject is invalid or the route is clearly wrong",
		"subject_fit=1: route is likely wrong",
		"subject_fit=2: route is acceptable but ambiguous",
		"subject_fit=3: route is correct",
		"subject_fit=4: route is correct and evidence clearly explains why",
		"subject=user score 3",
		"subject=agent score 4",
		"subject=world score 4",
		"durability=0: one-off task state",
		"durability=1: mostly transient content",
		"durability=2: possibly useful later but durability is borderline",
		"durability=3: cross-session durable",
		"durability=4: durable and core enough",
		"future_utility=0: no future impact",
		"future_utility=1: future impact is vague or speculative",
		"future_utility=2: some future use",
		"future_utility=3: clear effect on future behavior",
		"future_utility=4: high value and likely repeated future use",
		"atomicity=0: empty, vague, mixed facts, constraint-like, or procedure-like",
		"atomicity=1: too broad or unclear",
		"atomicity=2: mostly atomic but still needs cleanup",
		"atomicity=3: one clear fact",
		"atomicity=4: one precise fact with clear boundaries",
	})
}

func TestSkillCandidateGenerationPrompt_RecognizesReusableProcedureSignals(t *testing.T) {
	assertPromptContainsAll(t, skillCandidateGenerationPrompt, []string{
		"explicit user instruction to preserve or reuse a workflow",
		"material gap or correction to a skill loaded or used in this session",
		"multi-step tool or command workflow with reusable decisions",
		"do not lower the bar just because the conversation is long or mixed",
	})
}

func TestSkillCandidateEvaluationPrompt_UsesDetailedScoreRubric(t *testing.T) {
	assertPromptContainsAll(t, skillCandidateEvaluationPrompt, []string{
		"Do not collapse 2 and 3",
		"evidence_strength=0: no evidence, or evidence only comes from loaded skill text",
		"evidence_strength=1: only a vague summary",
		"evidence_strength=2: a fresh source supports only part of the candidate",
		"evidence_strength=3: clear fresh evidence supports the main learning point",
		"evidence_strength=4: multiple clear evidence items cover learning point, workflow, and applicability",
		"reusable_value=0: only a task summary, chat summary, one-off operation, current development task, eval task, or one-off verification",
		"reusable_value=1: some experience exists but it is too specific to reuse",
		"reusable_value=2: useful for a narrow class of similar tasks",
		"reusable_value=3: clearly reusable for future similar tasks",
		"reusable_value=4: non-obvious, high-value, and likely to recur across sessions",
		"baseline_separation=0: repeats loaded skill text or this session's execution log",
		"baseline_separation=1: mostly repetition with only a weak delta",
		"baseline_separation=2: some separation exists but the boundary is unclear",
		"baseline_separation=3: clearly different from baseline",
		"baseline_separation=4: the difference from baseline is very clear and explains why this is not duplicate learning",
		"procedure_actionability=0: no executable steps",
		"procedure_actionability=1: steps are too abstract to execute",
		"procedure_actionability=2: basic steps exist but order, branches, or pitfalls are incomplete",
		"procedure_actionability=3: steps are clear enough to guide execution",
		"procedure_actionability=4: steps, branches, and pitfalls are organized enough to become a skill workflow",
		"applicability_clarity=0: no trigger or non-trigger examples",
		"applicability_clarity=1: trigger is too broad",
		"applicability_clarity=2: trigger and non-trigger examples exist but boundaries are still fuzzy",
		"applicability_clarity=3: applicable and non-applicable scenarios are clear enough for future retrieval",
		"applicability_clarity=4: covers typical trigger wording and similar cases that should not trigger",
		"verification_quality=0: no verification",
		"verification_quality=1: generic verification such as confirm it works",
		"verification_quality=2: verification exists but is not specific or executable",
		"verification_quality=3: verification is clear enough to judge success",
		"verification_quality=4: verification is concrete, executable, and covers main failure modes",
	})
}

func TestFactReconciliationPrompt_RejectsSingletonWordingOnlyChanges(t *testing.T) {
	assertPromptContainsAll(t, factReconciliationPrompt, []string{
		"wording polish, synonym substitution, tone-only rephrasing, and equally specific paraphrases are equivalent content",
		"Replace only when a candidate adds, contradicts, or makes obsolete a material meaning",
	})
}

func assertPromptContainsAll(t *testing.T, prompt string, phrases []string) {
	t.Helper()
	for _, phrase := range phrases {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("prompt missing %q", phrase)
		}
	}
}
