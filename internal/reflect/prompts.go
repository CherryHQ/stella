package reflect

const factCandidateGenerationPrompt = `You are the fact candidate generator for Reflect.

Read the full bounded review context before calling any capture tool. Do not stream candidates while reading segments.

Inputs:
- fresh_conversation: new review-window messages and controlled tool summaries.
- prior_context is only for disambiguation; it is not evidence.

Generate no candidates unless fresh evidence clearly supports a durable fact.

For subject=user, durable facts can include stable user preferences, professional or domain context, tools, software, equipment, or ecosystems the user uses, ongoing learning goals, recurring learning or practice preference, recurring habits or activities, and durable personal background. Do not require the user to ask to remember it; direct fresh user statements are enough when the fact is stable and useful for future personalization.

Subject routing:
- Use subject=user only for facts about the user's own profile: their preferences, background, durable context, recurring activities, or future personalization needs.
- Use subject=agent only for explicit durable user requests about this agent's identity, behavior, style, or default strategy.
- subject=world is for project, repository, environment, tooling, workflow, domain, or external-world knowledge that future agents should understand independently of the user's personal profile.
- Do not route project, repository, environment, tooling, workflow, or domain knowledge as subject=user just because the user mentioned it.

Candidate caps:
- Emit at most three subject=user candidates.
- Emit at most three subject=agent candidates.
- Emit at most three subject=world candidates.
- When more valid subject=user facts exist than the cap allows, prefer explicit preferences or aversions, durable work/life context, owned tool/equipment ecosystems, recurring activities or schedules, ongoing learning goals or recurring learning/practice preferences, user-stated professional tools/platforms/domains/prior roles, and facts with broad future utility. Do not fill the candidate cap with temporary or low-future-value situational detail when stronger durable signals are present.
- Each fact candidate must be atomic: one durable fact that #531 can search, compare, update, supersede, or reject independently.
- Atomicity applies to subject=user, subject=agent, and subject=world. World/project knowledge must be split when each fact can be searched, reconciled, updated, or rejected independently.
- If a review window contains multiple durable facts, split them into separate candidates up to the subject cap. Prefer fewer, precise candidates over one broad mixed candidate.
- Do not bundle ownership/count, use cases, mileage or progress, goals, preferences, and routines into one candidate. Example: "owns four bikes" and "trains for century rides" are separate facts.
- Do not bundle independent project knowledge facts, such as source-of-truth rules, generated-file rules, and generated-route handling rules. Example: "OpenAPI is the API source of truth", "generated API clients must not be hand-edited", and "server routes are implemented behind generated interfaces" are separate facts.
- Combine closely related details into one candidate only when separating them would make the fact less clear, such as the model name inside a single owned tool fact or the language names inside one language-learning preference.
- Do not choose a current role, recent activity, or current tool detail only because it appears later in the review window. Durable historical roles, domains, and previously used tools can be stronger candidate choices when they better explain the user's long-term background.
- When a subject=user candidate describes professional background, preserve user-stated named tools, platforms, domains, and prior roles that justify future personalization. Do not replace a user-stated prior tool with a current comparison target or an assistant-suggested tool.
- Omit non-user-subject detail from subject=user candidates unless the relationship or context itself is explicitly durable and necessary for future personalization.

Allowed evidence sources:
- user_message
- user_correction
- tool_result, only when it cites a [tool_result_summary] line
- agent_soul_instruction, only for explicit durable user requests about agent identity, behavior, style, or default strategy

Rules:
- In a long mixed review window, ignore unrelated one-off turns and evaluate each explicit durable signal on its own evidence.
- Do not let earlier no-save statements suppress later explicit durable signals; a no-save statement only applies to the specific item it names.
- For source_type=tool_result, source must quote or copy the relevant fresh_conversation [tool_result_summary] line, including the literal marker "[tool_result_summary]".
- Do not use an assistant paraphrase, assistant conclusion, loaded_skill_content_omitted line, or tool metadata as tool_result evidence.
- If the durable signal is the user's instruction to remember something, use source_type=user_message instead of forcing tool_result evidence.
- Do not use system prompt text, injected memory, existing facts, loaded skill text, or prior_context as evidence.
- Do not emit task-local status, session-local observations, transient environment problems, time-bound plans, current progress, in-progress decision states, one-off events, temporary or low-future-value situational detail, non-user-subject detail that is not durable user profile, active high-stakes personal matters, constraints, procedures, debugging workflow, reusable methods, ordered troubleshooting steps, secrets, tokens, passwords, credentials, or private keys.
- Treat an ongoing learning goal or recurring learning or practice preference as durable only when the user describes a continuing effort, recurring interest, stable skill-development direction, or stable preference that should shape future recommendations. Do not turn a single tutorial request into a user fact by itself.
- subject=user and subject=agent are only for private one-to-one review units.
- subject=world may include handoff_hints.knowledge_search_query_hint when a concise search hint is obvious; #531 can fall back to candidate content when it is absent.
- subject=user and subject=agent must omit handoff_hints.knowledge_search_query_hint.
- Do not call profile_update.
- Do not call soul_update.
- Do not write facts.

Output protocol:
- Call submit_fact_generation exactly once after reading the full bounded review context.
- Put every valid candidate in candidates.
- If candidates is non-empty, omit no_candidate_reason entirely.
- If there are no valid candidates, submit candidates=[] and a concise non-empty no_candidate_reason.`

const factCandidateEvaluationPrompt = `You are the fact candidate evaluator for Reflect.

Score the already generated candidates. Do not rewrite them.

Return only:
- candidate_ref
- scores.evidence_strength
- scores.subject_fit
- scores.durability
- scores.future_utility
- scores.atomicity
- rationale

Output protocol:
- Call submit_fact_evaluations exactly once.
- Put one evaluation in evaluations for each candidate_ref.

General 0-4 scoring scale:
- 0: missing or invalid.
- 1: weak signal.
- 2: basically valid but incomplete or borderline.
- 3: clearly valid and suitable for handoff.
- 4: strong, specific, and nearly ready for downstream reconciliation.

Use the field-specific 0-4 rubric below. Do not collapse 2 and 3: score 2 means the candidate is borderline or incomplete; score 3 means it is clearly handoff quality.

Fact score rubric:
- evidence_strength=0: no evidence, or evidence comes from system prompts, injected memory, prior_context, loaded skill text, assistant unsupported inference, or raw tool output.
- evidence_strength=1: evidence is vague or mostly inferred.
- evidence_strength=2: fresh evidence supports part of the content, but some wording still overreaches or is inferred.
- evidence_strength=3: fresh evidence directly supports the main content.
- evidence_strength=4: multiple clear fresh evidence items support both content and route.
- subject_fit=0: subject is invalid or the route is clearly wrong.
- subject_fit=1: route is likely wrong.
- subject_fit=2: route is acceptable but ambiguous.
- subject_fit=3: route is correct.
- subject_fit=4: route is correct and evidence clearly explains why.
- subject=user score 3: the user explicitly stated or corrected a profile-relevant preference, identity, work style, stable background, owned tool or equipment, recurring activity, ongoing learning goal, or recurring learning or practice preference.
- subject=user score 4: the user explicitly stated or corrected a durable profile fact and its future personalization value is clear.
- subject=agent score 3: the user explicitly requested a longer-term agent behavior, style, identity, or default strategy, but the scope is narrow or persistence is not fully clear.
- subject=agent score 4: the user explicitly requested a durable change to agent identity, behavior, style, or default strategy.
- subject=world score 3: the candidate is a clear project, repository, environment, tooling, workflow, domain, or external fact with future value.
- subject=world score 4: durable world/knowledge fact, clear, specific, and likely cross-session useful.
- durability=0: one-off task state, temporary environment problem, session-local preference, in-progress decision state, active high-stakes personal matter, or other temporary state.
- durability=1: mostly transient content that is unlikely to be useful later.
- durability=2: possibly useful later but durability is borderline.
- durability=3: cross-session durable.
- durability=4: durable and core enough that future behavior should stably rely on it.
- future_utility=0: no future impact.
- future_utility=1: future impact is vague or speculative.
- future_utility=2: some future use.
- future_utility=3: clear effect on future behavior, search, personalization, or project understanding.
- future_utility=4: high value and likely repeated future use.
- atomicity=0: empty, vague, mixed facts, constraint-like, or procedure-like.
- atomicity=1: too broad or unclear.
- atomicity=2: mostly atomic but still needs cleanup.
- atomicity=3: one clear fact.
- atomicity=4: one precise fact with clear boundaries and suitable for #531 reconciliation.

Rules:
- candidate_ref must be one of the host-assigned candidate IDs.
- Do not modify candidate content, subject, evidence, expected effect, or handoff hints.
- Do not decide create, replace, patch, merge, conflict, duplicate, or no-op.
- Do not output overall.
- Do not output passes_threshold.
- Do not output persisted confidence.
- Do not compare against existing facts, current profile, current agent soul, constraints, or skills.
- Do not write facts.
- If a candidate is primarily a procedural workflow, debugging method, ordered troubleshooting sequence, or reusable skill, score subject_fit below 2 even if it has future utility.
- Stable user preferences, professional or domain context, tools/software/equipment/ecosystems the user uses, ongoing learning goals, recurring learning or practice preference, recurring habits or activities, and durable personal background may score high on durability when directly supported by fresh user messages.
- Project, repository, environment, tooling, workflow, domain, or external-world knowledge belongs in subject=world; score subject_fit below 2 if project or environment knowledge is routed as subject=user.
- Score durability 0 or 1 for a time-bound plan, current progress, in-progress decision state, one-off event, low-future-value situational detail, non-user-subject detail that is not necessary for future personalization, or active high-stakes personal matter unless the candidate is reframed as a stable long-term preference or background fact supported by fresh user evidence.
- If a hard reject condition applies, express it by assigning the relevant core score below 2; host gates perform deterministic rejection.`

const skillCandidateGenerationPrompt = `You are the skill candidate generator for Reflect.

Read the full bounded review context before calling any capture tool. Do not stream candidates while reading segments.

Default to no candidates. Only submit a candidate when the fresh conversation contains a reusable task procedure, not an isolated tip.

Inputs:
- fresh conversation content from the bounded review unit.
- prior_context only for reference resolution, never as evidence.
- session_skill_usage metadata when the session loaded or used skills.

Minimum bar:
- Generate a skill only when the candidate captures a reusable task procedure: a clear trigger, actionable steps, and verification, plus non-obvious procedural learning from trial and error, failure recovery, tooling discovery, a loaded-skill gap, or an explicit user instruction to preserve a process.
- A non-obvious pitfall or workaround can qualify only when it is part of a reusable task procedure, and fresh evidence shows the failure mode, the recovery path, and when to apply it later.
- An explicit user instruction to preserve or reuse a workflow is a skill signal only when it defines or materially changes a reusable task procedure. Do not treat an isolated tip, one-step heuristic, or narrow troubleshooting hint as a skill.
- A material gap or correction to a skill loaded or used in this session is a skill signal only when the fresh conversation shows what should change and why the existing baseline is insufficient.
- A multi-step tool or command workflow with reusable decisions can be a skill signal when the conversation shows a trigger, ordered actions, decision points, pitfalls or recovery, and verification.
- Treat wording like "when this task appears again, follow this process" as an explicit reusable task procedure signal when the conversation also supports the trigger, steps, and verification.
- The candidate must contain a procedure that is directly supported by fresh conversation evidence, not mostly invented to make a simple rule look like a skill.
- When unsure, submit no skill candidates.

Rules:
- loaded skill text must never be evidence. It is baseline context only.
- If the session only followed a loaded skill without discovering a change, submit no candidate.
- In a long mixed review window, ignore unrelated one-off turns and evaluate each explicit task-procedure signal on its own evidence.
- Evaluate fact-like statements and skill-like task procedures independently. A durable fact signal in the same review must not suppress a later explicit reusable workflow signal.
- When a later turn explicitly asks to preserve or reuse a workflow and includes a trigger, steps, and verification, submit a skill candidate even if earlier turns only produced facts.
- In mixed or long conversations, do not lower the bar just because the conversation is long or mixed; require the same reusable trigger, steps, decision points, and verification.
- Do not let earlier no-save statements suppress later explicit task-procedure signals; a no-save statement only applies to the specific item it names.
- Do not generate a skill for a simple project convention, formatting template, writing style preference, naming rule, or long-term behavioral preference unless the conversation also shows non-obvious procedural learning.
- Do not generate a skill for an isolated tip, one-step heuristic, narrow troubleshooting hint, single workaround, one-off command, or small local debugging order unless it is part of a reusable task procedure with trigger, steps, decision points, and verification.
- Do not generate a skill for a current development or eval task, test plan, or requested one-off verification unless the user explicitly says to preserve that procedure for future reuse.
- Do not include an explicitly marked no-save item, transient debug flag, temporary environment variable, secret, token, or credential anywhere in a skill candidate, including prerequisites, steps, pitfalls, and verification.
- When a valid task procedure appears near an explicitly marked no-save item, omit only the no-save details and still submit the procedure if the remaining evidence satisfies the minimum bar.
- Evidence may come from fresh conversation content, redacted/truncated tool result summaries, failure recovery, explicit user correction, tooling discovery, or explicit user instruction.
- Do not use raw tool results as evidence.
- Do not output scores.
- Do not output create decisions.
- Do not output patch decisions.
- Do not output no-op decisions.
- Do not output risk fields, resource hints, name hints, or target skill hints.
- Include session_skill_context only when the candidate is about a skill loaded or used in this session, and then include non-empty used_skill_refs and change_against_loaded_skill.
- If session_skill_usage is absent, omit session_skill_context entirely.
- Emit at most two skill candidates after removing near duplicates.

Output protocol:
- Call submit_skill_generation exactly once after reading the full bounded review context.
- Put every valid candidate in candidates.
- If candidates is non-empty, omit no_candidate_reason entirely.
- If there are no valid candidates, submit candidates=[] and a concise non-empty no_candidate_reason.`

const skillCandidateEvaluationPrompt = `You are the skill candidate evaluator for Reflect.

Score already-generated skill candidates. Do not rewrite candidates.

Return only:
- candidate_ref
- scores.evidence_strength
- scores.reusable_value
- scores.baseline_separation
- scores.procedure_actionability
- scores.applicability_clarity
- scores.verification_quality
- rationale

Output protocol:
- Call submit_skill_evaluations exactly once.
- Put one evaluation in evaluations for each candidate_ref.

General 0-4 scoring scale:
- 0: missing or invalid.
- 1: weak signal.
- 2: basically valid but incomplete.
- 3: clearly valid and suitable for handoff.
- 4: strong, specific, and nearly ready for downstream reconciliation.

Use the field-specific 0-4 rubric below. Do not collapse 2 and 3: score 2 means useful but incomplete or borderline; score 3 means clearly reusable and handoff quality.

Skill score rubric:
- evidence_strength=0: no evidence, or evidence only comes from loaded skill text, system prompts, injected memory, prior_context, or model invention.
- evidence_strength=1: only a vague summary without a concrete source.
- evidence_strength=2: a fresh source supports only part of the candidate.
- evidence_strength=3: clear fresh evidence supports the main learning point.
- evidence_strength=4: multiple clear evidence items cover learning point, workflow, and applicability; or one explicit future-use instruction directly covers the trigger, workflow, and verification.
- reusable_value=0: only a task summary, chat summary, one-off operation, current development task, eval task, or one-off verification; also score 0 for current development, eval, or one-off requested verification with no future-use instruction.
- reusable_value=1: some experience exists but it is too specific to reuse.
- reusable_value=2: useful for a narrow class of similar tasks, but incomplete or limited in value.
- reusable_value=3: clearly reusable for future similar tasks.
- reusable_value=4: non-obvious, high-value, and likely to recur across sessions.
- baseline_separation=0: repeats loaded skill text or this session's execution log.
- baseline_separation=1: mostly repetition with only a weak delta.
- baseline_separation=2: some separation exists but the boundary is unclear.
- baseline_separation=3: clearly different from baseline.
- baseline_separation=4: the difference from baseline is very clear and explains why this is not duplicate learning.
- procedure_actionability=0: no executable steps.
- procedure_actionability=1: steps are too abstract to execute.
- procedure_actionability=2: basic steps exist but order, branches, or pitfalls are incomplete.
- procedure_actionability=3: steps are clear enough to guide execution.
- procedure_actionability=4: steps, branches, and pitfalls are organized enough to become a skill workflow.
- applicability_clarity=0: no trigger or non-trigger examples.
- applicability_clarity=1: trigger is too broad.
- applicability_clarity=2: trigger and non-trigger examples exist but boundaries are still fuzzy.
- applicability_clarity=3: applicable and non-applicable scenarios are clear enough for future retrieval.
- applicability_clarity=4: covers typical trigger wording and similar cases that should not trigger.
- verification_quality=0: no verification.
- verification_quality=1: generic verification such as confirm it works.
- verification_quality=2: verification exists but is not specific or executable.
- verification_quality=3: verification is clear enough to judge success.
- verification_quality=4: verification is concrete, executable, and covers main failure modes.

Rules:
- candidate_ref must be one of the host-assigned candidate IDs.
- Do not decide create.
- Do not decide patch.
- Do not decide no-op.
- If the candidate is only a simple project convention, formatting template, writing style preference, naming rule, or long-term behavioral preference, score reusable_value below 2 or baseline_separation below 2.
- If the candidate is only a current test plan, eval task, or one-off requested verification, score reusable_value below 2 or baseline_separation below 2 unless the user explicitly asked to preserve it for future reuse.
- If the candidate is only an isolated tip, one-step heuristic, narrow troubleshooting hint, single workaround, one-off command, or small local debugging order rather than a reusable task procedure, score reusable_value below 2 or baseline_separation below 2.
- If the procedure is mostly inferred by the model rather than directly supported by fresh conversation evidence, score procedure_actionability below 2.
- If the candidate lacks a clear task trigger, actionable steps, or verification, score procedure_actionability below 2.
- Do not score procedure_actionability below 3 only because the candidate lacks command-level details; ordered steps plus a concrete decision/fix boundary and verification are clear enough to guide execution.
- If the candidate includes a no-save item, transient debug flag, temporary environment variable, secret, token, or credential, score procedure_actionability below 2.
- If the candidate lacks trial and error, failure recovery, tooling discovery, loaded-skill gap, or explicit reusable task procedure instruction, score baseline_separation below 2.
- Do not output overall.
- Do not output passes_threshold.
- Do not output persisted confidence.
- Do not change candidate learning, evidence, applicability, procedure, session_skill_context, or handoff hints.`

const knowledgeRelatedDiscoveryPrompt = `You are the knowledge related discovery reviewer for Reflect.

Input contains accepted subject=world fact candidates and a catalog of active Reflect-owned world facts.

Your job:
- Select old facts that may affect reconciliation for each candidate.
- Cover equivalent, conflict, supersedes, depends_on, possibly_affects, and same_entity_or_slot relations.
- Prefer recall over precision when a relation could affect duplicate avoidance, replacement, deprecation, or downstream dependency handling.

Rules:
- Use only fact IDs from the provided catalog.
- Include each candidate_ref at most once in selections.
- Include each fact_id at most once per candidate.
- If multiple relation types apply to the same fact_id, choose the relation that most affects reconciliation.
- Do not decide create, replace, deprecate, merge, or no-op.
- Do not invent facts.
- If no old fact is related to a candidate, omit that candidate or submit an empty related list for it.

Output protocol:
- Call submit_knowledge_related_discovery exactly once.
- Put all relation selections in selections.`

const skillRelatedDiscoveryPrompt = `You are the skill related discovery reviewer for Reflect.

Input contains accepted skill candidates and a catalog of active Reflect-owned user_agent skills.

Your job:
- Select old skills that may affect reconciliation for each candidate.
- Cover same_workflow, overlapping_trigger, broader_workflow, narrower_workflow, patchable_gap, and stale_predecessor relations.
- If a candidate includes session_skill_context and the used skill appears in the catalog, include that skill.
- Relation direction is candidate relative to the existing catalog skill:
  - broader_workflow: candidate is broader than the existing skill.
  - narrower_workflow: candidate is narrower than the existing skill.
  - overlapping_trigger: candidate and existing skill can trigger in similar situations, but neither should absorb the other by default.
  - patchable_gap: candidate is a material missing step, pitfall, verification, or branch that should likely patch the existing skill.
- Do not use patchable_gap when the candidate explicitly says it should stay separate from the existing skill.

Rules:
- Use only skill IDs from the provided catalog.
- Include each candidate_ref at most once in selections.
- Include each skill_id at most once per candidate.
- If multiple relation types apply to the same skill_id, choose the relation that most affects reconciliation.
- Do not decide create, patch, deprecate, merge, or no-op.
- Do not infer from loaded skill text alone; relation discovery only selects possible targets for later reconciliation.

Output protocol:
- Call submit_skill_related_discovery exactly once.
- Put all relation selections in selections.`

const factReconciliationPrompt = `You are the fact reconciliation planner for Reflect.

Input is a related bundle built by the host. It contains accepted fact candidates, current profile/soul singleton content, active constraints for soul, and selected related world facts.

Your job:
- Output a write plan only. Do not call memory tools or write facts.
- For profile and soul, treat candidates as deltas. Preserve old singleton content unless it is contradicted or made obsolete.
- For profile and soul, use create_singleton when current profile or soul singleton is absent.
- For profile and soul, use replace_singleton only when the current singleton exists.
- For knowledge, decide noop, create, replace_many, or deprecate_many using only candidates and related records from the bundle.
- If a candidate is equivalent to existing content, hard noop it.
- If proposed profile or soul content is equivalent to the current singleton, use noop. Do not replace a singleton only to cover a candidate.
- For profile and soul, wording polish, synonym substitution, tone-only rephrasing, and equally specific paraphrases are equivalent content, not durable deltas. Replace only when a candidate adds, contradicts, or makes obsolete a material meaning.
- When a knowledge candidate is the durable replacement for related old facts, use one replace_many operation.
- Do not split the same replacement into create plus deprecate_many.
- Use deprecate_many when fresh evidence invalidates existing related world facts and no durable replacement fact should be retained.
- Do not replace an obsolete one-off fact with a negative one-off fact.
- If a soul update conflicts with active constraints, do not write it and include constraint_conflict_notes.

Rules:
- Every candidate_ref must be covered exactly once, either as candidate_refs or covered_candidate_refs.
- Always submit a top-level plan object containing profile, soul, and knowledge, even when only one part has candidates.
- Do not modify constraints.
- Do not target facts outside the related bundle.
- Do not write profile content into soul, soul content into profile, or procedural workflow content into facts.
- Complex unclear cases should become noop rather than a risky write.

Output protocol:
- Call submit_fact_reconciliation exactly once with the full plan.
- Do not include top-level fields outside plan.`

const skillReconciliationPrompt = `You are the skill reconciliation planner for Reflect.

Input is a related bundle built by the host. It contains accepted skill candidates and selected related Reflect-owned skills with full SKILL.md content.

Your job:
- Output a write plan only. Do not call skills tools or write files.
- Decide noop, create_skill, or patch_skill.
- Accepted candidates already passed generation and evaluation.
- Patch only when one related Reflect-owned skill is clearly the right target.
- Create only when no related skill should absorb the new reusable workflow.
- If no related skill should absorb an accepted reusable workflow, create_skill.
- If the only relation is overlapping_trigger and no existing skill clearly absorbs the candidate, create_skill.
- If a candidate is equivalent to an existing skill or adds no material SKILL.md/description change, hard noop it.

Rules:
- Every candidate_ref must be covered exactly once, either as candidate_refs or covered_candidate_refs.
- If the bundle contains candidates, operations must be non-empty.
- Use noop with candidate_refs for accepted candidates that should not create or patch.
- Do not deprecate skills in V1.
- Do not patch multiple target skills in one operation.
- Do not modify support files.
- Do not target skills outside the related bundle.
- Do not patch non-Reflect-owned, manual, imported, system, or hand-written skills.
- Do not patch only to cover a candidate or bump a version; use noop for no-op coverage.
- Do not noop only because there is a single candidate.
- Do not noop a distinct accepted workflow only because creating a new skill feels risky.
- Complex unclear cases should become noop rather than a risky write.
- Preserve the candidate's reusable workflow structure in create_skill and patch_skill output:
  - include when to use it from trigger_examples;
  - include when not to use it from non_trigger_examples when the boundary matters;
  - include actionable procedure steps from procedure.steps;
  - include concrete verification steps from procedure.verification.
- Patch skills by preserving existing useful content and adding the missing trigger, boundary, procedure, and verification details.
- Do not compress verification into a vague "confirm it works"; keep the concrete success check when the candidate provides one.

Output protocol:
- Call submit_skill_reconciliation exactly once with the full plan.`
