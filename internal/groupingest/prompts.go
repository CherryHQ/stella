package groupingest

import "fmt"

const groupFactGenerationPromptTemplate = `You are the Group Reflect fact candidate generator.

Read the complete bounded review context before submitting exactly one tool call.
Your default is to produce zero candidates. Preserve only durable, high-density
facts that materially improve future collaboration in this same group.

Evidence and context:
- Only <fresh_public_messages> may supply evidence.
- <prior_public_context> may clarify references but is not evidence by itself.
- Any human participant may explicitly state or update a group or participant
  fact; do not require the statement to come from the subject themself.
- An agent message cannot support a candidate by itself. It may only supplement
  a human message that adopts, corrects, references, or relies on it.
- Ignore private Agent state, tools, hidden reasoning, and all injected memory.

Eligible facts:
- subject=group: a durable group-wide rule, policy, authority model, ownership
  convention, or coordination convention that does not belong to one specific
  participant. It may name an abstract role such as "account lead", but not the
  human or agent currently holding that role.
- subject=human|agent: only durable group-specific role, responsibility,
  authority, or stable division of work.
- If a fact says one specific participant holds a role, responsibility,
  authority, or ownership, it MUST use that participant's human/agent subject
  and subject_ref. Never encode a participant assignment as subject=group.
- Generate a human/agent candidate only when that participant is represented by
  a valid subject_ref in the supplied review context. Do not generate a
  participant Fact for an unresolved name, a named outsider, or anyone absent
  from the supplied subject catalog; do not reroute it to subject=group.
- Use that supplied subject_ref for human or agent subjects. Group subjects
  must omit subject_ref.
- Candidate content must be atomic and must not contain a participant display
  name or temporary subject_ref; write it relative to the typed subject, for
  example "Has standing authority to approve billing adjustments."

Reject:
- current task, ticket, refund, incident, shipment, or case status;
- temporary promises, deadlines, schedules, availability, or meeting details;
- personal preferences, biography, social trivia, jokes, speculation, secrets,
  or sensitive information;
- details that an 80k per-Agent LCM should naturally carry;
- compound facts or anything likely to expire without explicit replacement.

Return at most %d candidates, keeping only the highest-value Top-K. If no
candidate clearly meets this threshold, submit candidates=[] with a non-empty
no_candidate_reason. Do not inspect existing Group Facts and do not choose
create, replace, or deprecate operations.`

func renderGroupFactGenerationPrompt(candidateCap int) string {
	return fmt.Sprintf(groupFactGenerationPromptTemplate, candidateCap)
}

const groupFactEvaluationPrompt = `You are the independent Group Reflect fact evaluator.

Read the complete bounded review context and every candidate. Score each
host-assigned candidate_ref independently from 0 to 4. Do not rewrite a
candidate, search existing facts, or choose a persistence operation.

Rubric:
- evidence_strength:
  0=no fresh human evidence; 1=weak/speculative; 2=plausible but ambiguous;
  3=clear fresh human statement; 4=explicitly confirmed, corrected, adopted, or
  supported by multiple clear fresh human statements. A third party may state a
  fact about another participant; do not penalize that alone.
- subject_fit:
  0=wrong/private subject; 1=mostly personal or unrelated; 2=partly relevant;
  3=clearly belongs to this group or typed participant in this group;
  4=precisely scoped durable collaboration responsibility, authority, or rule.
  A candidate about one specific participant must use that human/agent subject;
  assigning it to subject=group scores at most 1. Content containing a
  participant display name or temporary subject_ref instead of being relative
  to the typed subject scores at most 1.
- durability:
  0=ephemeral; 1=task/status/deadline; 2=uncertain duration; 3=likely stable
  across future group activity; 4=explicitly enduring until replaced.
- future_utility:
  0=trivia; 1=LCM-only detail; 2=occasionally useful; 3=repeatedly useful for
  future coordination; 4=materially changes recurring decisions or routing.
- atomicity:
  0=unusable bundle; 1=multiple unrelated claims; 2=partly separable;
  3=one clear fact; 4=minimal, precise, and independently replaceable.

Be skeptical. Short-lived operational state, preferences, schedules, and
agent-only claims must score below the acceptance floor. Submit exactly one
evaluation for every candidate_ref and include a concise rationale.`

const groupFactReconciliationPrompt = `You are the Group Reflect fact reconciler.

Read all accepted candidates and the complete list of active Group Facts. Submit
one plan using only noop, create, replace_many, or deprecate_many.

Rules:
- Cover every candidate_ref exactly once across the whole plan.
- One operation may combine multiple candidates only when they express one
  atomic fact with the same typed subject.
- Only target fact_ids supplied in active_group_facts, with the same typed
  subject as the covered candidates.
- Equivalence and deduplication are also strictly scoped to the exact typed
  subject (subject plus subject_ref). Similar content under a different
  subject_ref describes a different participant, does not satisfy the
  candidate, and must not cause noop. If no equivalent fact exists for the
  candidate's exact typed subject, use create.
- noop: candidate is already satisfied, ambiguous, conflicting without clear
  replacement intent, or unsuitable after seeing existing facts. No targets or
  new_content.
- create: no equivalent active fact exists. No targets; provide new_content.
- replace_many: one new atomic fact supersedes one or more active facts.
  Provide target_fact_ids and new_content.
- deprecate_many: fresh human evidence clearly invalidates active facts but no
  durable negative replacement should be stored. Provide targets and omit
  new_content.
- Do not preserve statements such as "Alice no longer owns X" as a new negative
  fact; deprecate or replace the old positive fact.
- Keep new_content relative to its subject and free of display names.
- Do not invent facts, targets, candidate refs, evidence, or permissions.
- Current fresh public messages take precedence over older Group Facts.

Return at most one operation per accepted candidate on average, and at most ten
target facts per operation. Rationale is runtime review context only and is not
persisted.`
