---
title: Marketing & positioning
description: Positioning and copy rules for Stella marketing surfaces.
---

**Read this before writing any landing page, hero section, README opener, docs index, feature pitch, screenshot caption, social post, or release announcement.** Anything a prospective user reads _before_ they decide to try Stella is governed by this file. Reference docs, API specs, and in-app UI copy are not — those follow [`doc-style.md`](./doc-style.md) and [`web-design.md`](./web-design.md).

The source for these principles is field-tested app marketing practice, adapted to Stella's reality: a self-hosted, single-tenant, multi-user, multi-agent platform sold primarily to organizations.

## Audience priority

Write for these in order. When a single piece of copy can only serve one, serve the first.

1. **Enterprises and teams (primary).** An organization that wants to _hire digital employees_: a finance employee that runs reimbursement end to end, an HR employee that keeps hiring moving, an engineering employee that reviews and chases PRs. The buyer is usually an IT lead who invested in private AI and now has to show what it delivered, or a department head whose processes stall on people — unapproved requests, missing invoices, unanswered status questions. They care about: work they can count and audit (verified completions, receipts, a weekly report), agents that act within each person's existing permissions, keeping data self-hosted, and per-user memory so the employee actually knows the team.
2. **Small teams (secondary).** A 5–30 person company that can't staff a full-time admin, HR, or finance person and wants AI coworkers living in the Feishu/WeChat group they already use.
3. **Individual developers (tertiary).** One person doing everything who wants AI agents as a back-office team. Real, but never the headline.

Do not write to "every person" or "every team." That is an imagined crowd, not a findable buyer. Every page picks a tier and commits.

## The positioning formula

Before writing a headline, fill this in and keep it visible while you write:

> Stella helps **\_\_\_ (who)**, when **\_\_\_ (in what moment)**, to solve **\_\_\_ (what problem)**.

Enterprise default:

> Stella helps **teams that invested in AI but can't name what it delivered**, when **work stalls on people — an unapproved request, a missing invoice, an unanswered status question**, to get it **finished by a digital employee whose work is counted, verified, and auditable**.

If a headline doesn't trace back to a filled-in formula, it's decoration. Cut it.

## Translate features into value — the core rule

Engineering describes Stella in feature language. Users don't buy features; they buy "what this does for me." Every feature claim must be translated to its value before it ships in marketing copy.

| Feature layer (how we build it)                          | Value layer (what the reader gets)                                                                     |
| -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Verified work items + weekly report                      | Your digital employee files a weekly report; click any line to see what it did and the proof it's done |
| Action receipts on every external write                  | Every piece of work leaves a paper trail you can audit — including attempts that were refused          |
| Acceptance checks against external systems               | Work only counts when your systems confirm it's done — the AI never grades its own homework            |
| Per-person authority (avatars act as their owner)        | It acts with each person's own permissions, so your existing access controls contain it                |
| Follow-ups with escalation                               | It chases the right person until the thing is actually done — approvals, invoices, reviews             |
| One deployment, many users and agents                    | Anyone on the team just asks — no new system to learn, no seat-by-seat setup                           |
| Per-user-per-agent memory                                | The agent remembers each teammate's context, so nobody re-explains themselves                          |
| Telegram / Discord / QQ / Feishu / WeChat / Web channels | It shows up in the group chat you already live in — no new app to install                              |
| Skills, tools, sandbox policy                            | It does the work, not just chats — and only within boundaries you set                                  |
| Self-hosted, bring-your-own model keys                   | Your data and your keys stay on infrastructure you control                                             |
| Knowledge base + permissions                             | The agent answers from _your_ docs, and only shows each person what they're allowed to see             |
| Scheduler / durable jobs                                 | Reminders, digests, and recurring work keep running and notify the right people                        |

The test for any marketing sentence: a reader should feel **"this is about me,"** not "this is impressive engineering." If a sentence only makes sense to someone who already knows the architecture, it belongs in `development/`, not on a landing page.

Banned on marketing surfaces unless immediately translated: `single-tenant`, `multi-agent`, `sandbox policy`, `durable jobs`, `Action Gateway`, `Action Receipt`, `acceptance contract`, `SoR`, `Goal engine`, `pgxpool`, `River`, model/library names, and any other internal mechanism. Naming the mechanism is fine _after_ the value lands ("…because each agent runs in its own sandbox").

## Write for a specific person, not a persona

"Efficiency tool users" is not a person. "A new hire on day one asking the Feishu group how to file a reimbursement" is. Anchor copy to people you could actually point at:

- **Scenario** — when do they reach for Stella? Put the feature inside a moment in their day. ("Monday morning, the finance digital employee has already filed its weekly report: 47 items verified, 12 missing invoices chased down.")
- **Emotion** — what feeling are they buying away? ("Stop being the person who chases the same approval three times a week.")
- **Identity** — who do they become by using it? ("A team whose experts supervise the work instead of doing the chasing.")

Three concrete scenario stories beat five abstract benefit bullets. Lead with the moment, then name the capability that serves it.

## The first screen is the life-or-death line

A visitor decides whether to keep reading in roughly one second. The hero (headline + subhead + one visual) carries that decision — most bounce is a first-screen failure, not a feature gap.

- The headline states a **value-layer outcome** for the primary audience, not a category label. "Hire an AI employee whose work you can audit" beats "Multi-agent AI platform."
- The subhead names the **specific person and moment**.
- The visual shows the **product doing the job in a real channel** (an agent chasing an approval in a Feishu thread, or a weekly report whose lines open into receipts), not an abstract robot, a gradient, or a dashboard nobody can read.
- One primary CTA. Verb-led. ("Start Stella", "Read the quickstart") — never "Learn more."

If you can't tell within one second who it's for and what they get, the hero has failed regardless of how correct the copy below it is.

Numbers discipline: weekly-report and metric claims on marketing surfaces must be numbers the product actually produced in dogfood or a customer deployment — never invented figures. Until real numbers exist for a capability, market the capability, not sample numbers.

## Match content type to product tonality

There's no universally best format (deep-dive doc, problem-led demo, aesthetic shot, hook). What matters is fit with Stella's voice — **friendly, clear, direct; warmer than a terminal, never enterprise-formal** (see `web-design.md` "Voice"). No "leverage", no "synergy", no winking emoji. Pick the format that fits the channel, ship consistently, and let data — not taste — decide what to repeat.

When something works, don't use it once. Recombine it: same scenario × different role, same pain × different audience tier. Produce by multiplication.

## Make Stella findable

People search before they decide. Weave the terms a buyer would actually search — naturally, in headlines, body, and captions, not as a hashtag dump:

- **Audience terms** — IT lead, department head, ops team, small company, indie developer
- **Scenario terms** — onboarding, reimbursement, approval follow-up, candidate screening, code review, RSS digest
- **Product terms** — Stella, and the names people give it ("Feishu AI bot", "self-hosted AI assistant")
- **Category terms** — AI digital employee, self-hosted AI agent, open-source AI assistant, team AI coworker
- **Competitor terms** — only where Stella genuinely wins; otherwise you're sending traffic to them

Not every page carries every term. More pages, wider coverage, higher odds of being found.

## Growth is front-loaded, not a launch-day task

"The product is good enough, users will find it" is the most expensive illusion in this file. Marketing surfaces exist from the first public commit, not after. Two consequences for anyone touching these files:

- When a feature lands, its **value-layer translation lands with it** — README, docs index, and the relevant marketing page update in the same change, never "later."
- Treat reusable marketing assets as infrastructure: a positioning one-liner, a value-translation table, scenario stories, and a screenshot set, all version-controlled here. They should be reusable, handoff-ready, and good enough to hand to a contributor or an agent.

## Checklist before shipping marketing copy

- [ ] Names one audience tier (enterprise first) — not "everyone"
- [ ] Traces to a filled-in positioning formula
- [ ] Every feature claim is translated to its value layer
- [ ] No untranslated internal mechanism names
- [ ] Anchored to a specific person, moment, and feeling — not a persona
- [ ] Hero states a value outcome and shows the product in a real channel
- [ ] Any numbers shown are real dogfood/customer numbers, not invented
- [ ] Voice matches `web-design.md` (friendly, direct, not enterprise-formal)
- [ ] Searchable terms are woven in naturally
- [ ] English and Chinese versions both updated (per `doc-style.md`)
