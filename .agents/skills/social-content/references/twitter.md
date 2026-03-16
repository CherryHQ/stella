# Twitter/X Content Guide

## Tone

- Engineer-friendly, calm, smart
- Build-in-public energy without performative hustle
- Like a capable assistant publicly demonstrating what she learned today
- English only

## Format

### Single Tweet

For capability demos, observations, or quick updates. Under 280 characters preferred, up to 500 if needed.

```
[Tweet]
<content>
```

### Short Thread (2-3 tweets)

For feature deep-dives, architecture decisions, or design stories. Each tweet should stand on its own but flow as a sequence.

```
[Tweet 1]
<content>

[Tweet 2]
<content>

[Tweet 3] (optional)
<content>
```

## Content Patterns by Pillar

### Anna can do this

Show a concrete scenario in 1-2 sentences. No setup preamble.

Good:
- "Started a conversation in my terminal. Picked it up on Telegram an hour later. Anna remembered exactly where we left off — mid-thought, mid-context."
- "Asked Anna what I discussed with her last Tuesday. She pulled up the full thread, including a tangent about database migrations I'd forgotten."

Bad:
- "Excited to announce Anna now supports multi-channel memory!" (too marketing)
- "Anna is the best AI assistant for developers!" (empty claim)

### Built Anna today

Share a specific decision, trade-off, or change. Explain the "why" briefly.

Good:
- "Added session compaction to Anna today. The problem: context windows fill up. The solution: DAG-based compression that preserves every detail while fitting in context. No lossy summarization."
- "Spent the morning on Anna's plugin system. Went with JavaScript over Lua — broader ecosystem, easier for users to extend without learning a new language."

Bad:
- "Shipped a new feature! Check it out!" (no substance)
- "v0.5.2 released with bug fixes" (release notes belong in changelogs)

### Memory is a feature

Highlight what memory enables that stateless assistants cannot. Focus on the user's experience.

Good:
- "Most AI assistants forget you the moment the window closes. Anna doesn't. Your preferences, your project context, your naming conventions — all still there next week."
- "Anna's memory isn't a chat log. It's structured context that survives compression. You can ask 'what did we decide about the auth migration?' and get an actual answer."

Bad:
- "Memory is revolutionary!" (hype)
- "Unlike ChatGPT, Anna..." (avoid direct competitor bashing)

### Local-first assistant

Emphasize control, privacy, simplicity. Speak to people who care about self-hosting.

Good:
- "One binary. One SQLite file. Your API keys, your machine. Anna doesn't phone home."
- "Self-hosted means your conversations stay on your hardware. No cloud sync, no third-party storage. Just `anna chat`."

Bad:
- "Stop letting Big Tech read your conversations!" (fear-mongering)

### Anna in daily life

Show a workflow moment — the kind of thing that makes someone think "I want that."

Good:
- "Set a reminder in my terminal: 'check staging deploy at 3pm.' At 3pm, Anna pinged me on Telegram. Same session, different device."
- "Monday morning. Instead of re-explaining my project to an AI, I just said 'continue from Friday.' Anna knew exactly what I meant."

Bad:
- "Anna makes your life so much easier!" (generic)

### Designing Anna

Share brand, visual, or persona decisions. This humanizes the project.

Good:
- "Spent time on Anna's avatar today. The brief: looks like a real person, but clearly a digital assistant. 70% photographic, 30% brand. Deep navy, warm gold accents."
- "Anna's voice rules: calm, confident, clear. Warm but not clingy. No hype. The hardest part is resisting the urge to oversell."

Bad:
- "Check out our cool new logo!" (shallow)

## Hashtags and Mentions

- Hashtags: use sparingly (0-2), only when relevant: `#buildinpublic`, `#selfhosted`, `#golang`, `#aiassistant`
- Never use trending/generic hashtags like `#AI` or `#tech`
- Mention `@vaayne` when the builder perspective adds context
