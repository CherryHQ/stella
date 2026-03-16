---
name: social-content
description: >
  Generate and post social media marketing content for Anna, a self-hosted AI assistant.
  Produce platform-specific post drafts for Twitter/X (English, technical credibility)
  or Xiaohongshu (Chinese, scenario-driven). Can post tweets to X via script (requires
  user approval). Use when the user asks to "write a post", "create social content",
  "draft a tweet", "write xiaohongshu content", "marketing post", "social media draft",
  "post to twitter", "post tweet", or discusses content for Anna's social accounts.
---

# Social Content for Anna

Generate on-brand social media drafts for Anna's Twitter/X and Xiaohongshu accounts.

## Anna — Brand and Product Reference

For full context on Anna's brand, product, visual identity, and content strategy, read [references/anna.md](references/anna.md). Always consult this reference when unsure about positioning, capabilities, or brand voice.

## Anna's Voice

All content MUST follow these voice rules:

- **Calm** — no hype, no urgency, no "game-changer" language
- **Confident** — state capabilities plainly, let the product speak
- **Clear** — one idea per post, no jargon without context
- **Warm but not clingy** — approachable, never desperate for engagement
- **Understated** — show, don't sell

### Never

- Overly cute, flirty, or emoji-heavy
- Corporate marketing speak or slogans
- News aggregator tone (reposting AI headlines)
- Virtual girlfriend/boyfriend framing
- Exaggerated claims ("revolutionary", "the future of AI")
- Content that implies Anna is a real human blogger

## Content Pillars

Each post fits one pillar. Ask the user which pillar, or infer from their topic:

| Pillar | Focus |
|---|---|
| **Anna can do this** | One capability demo — a sentence + a scenario |
| **Built Anna today** | Build in public — new feature, design decision, update log |
| **Memory is a feature** | Long-term context, info retrieval, conversation continuity |
| **Local-first assistant** | Self-hosted, data control, single binary, no cloud dependency |
| **Anna in daily life** | Reminders, notifications, cross-device workflow, daily routines |
| **Designing Anna** | Avatar, brand voice, visual identity, persona decisions |

### Content Ratio Target

- 70% product capabilities and scenarios (pillars 1, 3, 4, 5)
- 20% build in public and design (pillars 2, 6)
- 10% industry observations (only when explicitly requested)

## Workflow

1. Determine **platform** (`twitter` or `xiaohongshu`) — ask if not specified
2. Determine **pillar** — ask or infer from the user's topic
3. Read the platform-specific reference:
   - Twitter/X: [references/twitter.md](references/twitter.md)
   - Xiaohongshu: [references/xiaohongshu.md](references/xiaohongshu.md)
4. Draft the post following platform format and voice rules
5. Present the draft for user review — never assume auto-posting
6. **Posting to Twitter/X** (only when the user explicitly asks to post):
   a. Present the final tweet text and ask: *"Ready to post this to X? (yes/no)"*
   b. **Wait for explicit user approval** — do NOT proceed without a clear "yes"
   c. Run the posting script:
      ```bash
      uv run --script ./scripts/post_twitter.py "TWEET_TEXT"
      ```
   d. Use `--dry-run` first if the user wants to preview without posting:
      ```bash
      uv run --script ./scripts/post_twitter.py --dry-run "TWEET_TEXT"
      ```
   e. Report the result back to the user

   **Requirements:** `X_CLIENT_ID` and `X_CLIENT_SECRET` must be set in the environment. On first run, the script will prompt for OAuth2 authorization in the browser; tokens are cached at `~/.anna/x_tokens.json` for subsequent use.

## Quality Checklist

Before presenting any draft, verify:

- [ ] Matches Anna's voice (calm, confident, clear, warm)
- [ ] Fits one content pillar cleanly
- [ ] Factually accurate about Anna's capabilities
- [ ] Follows platform-specific format from the reference file
- [ ] No forbidden patterns (hype, cute, salesy, news-repost)
- [ ] Appropriate length for the platform
