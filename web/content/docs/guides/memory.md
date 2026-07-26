---
title: Memory
---

## What Stella Remembers

Stella remembers everything you tell her — across sessions, across days, across weeks. When you mention your name, your timezone, your preferred coding style, or that you hate unnecessary abstractions, Stella writes it down and recalls it the next time you chat.

Memory is scoped per user per agent, so each agent you create has its own understanding of who you are and what you care about.

Here is what Stella tracks:

- **Your profile** — name, preferences, habits, working style, and anything else you share about yourself.
- **Conversation history** — every message you exchange, searchable and recoverable even after long conversations.
- **Constraints** — rules you set that Stella must always follow, like "never delete files without asking."

## How Conversations Stay Manageable

As your conversations grow long, Stella automatically compresses older messages into summaries. You do not need to do anything — it happens in the background when the conversation gets large.

The key thing to know: **nothing is lost.** Summaries preserve the important details, and Stella can drill back into them if she needs the specifics. You can talk for weeks in the same session, and Stella will still recall what you discussed on day one.

If you start a new session, Stella carries forward your profile and constraints automatically. Past conversation content is available through search.

## Starting a Fresh Session

Sometimes you do not want a shorter conversation, you want a clean one. Two commands cover the difference:

- **`/new`** starts a fresh session. The previous one is archived, not deleted — it stays searchable, and your profile and constraints carry over.
- **`/compact`** keeps the session you are in and compresses its history, so the context gets shorter without losing the thread.

You can also just ask: "start a new session", "新会话". Because a reset clears the working context, Stella asks you to confirm first and resets only after you agree in your next message. Typing `/new` is consent on its own, so it takes effect immediately. Asking for compaction needs no confirmation.

In a group chat, every agent keeps its own session. `/new` resets the single agent in the group, and a group with several agents needs `/new @agent` so an unclear command never resets everyone. `/compact` does not apply to group chats — there, `/new` is the way to clear an agent's context.

In the Web UI you start a new session from the sidebar rather than with a command, because an open chat window stays on the session it was opened with.

## Managing Your Profile

Stella learns about you naturally through conversation. If you say "I'm in the Pacific timezone" or "I prefer TypeScript over JavaScript," she remembers.

You can also ask Stella to manage your profile directly:

- **"What do you know about me?"** — Stella reads back your stored profile notes.
- **"Update my profile: I've switched to using Neovim."** — Stella updates your profile.
- **"Forget that I prefer dark mode."** — Stella removes that detail.

Profile changes are versioned. If Stella updates your profile and you do not like the result, you can ask her to roll it back:

- **"Show my profile history."**
- **"Roll back my profile to the previous version."**

## Setting Constraints

Constraints are persistent rules that Stella follows in every session. They are stronger than preferences — they are hard rules that Stella checks before acting.

To add a constraint, tell Stella what you want:

- **"Always ask me before running destructive commands."**
- **"Never commit directly to the main branch."**
- **"Do not send notifications between midnight and 7am."**

Stella will confirm with you before saving a constraint. Once set, it applies to every future conversation with that agent.

To manage existing constraints:

- **"List my constraints."** — See all active rules.
- **"Remove the constraint about notifications."** — Delete a specific rule.

## Searching Past Conversations

You can search through your conversation history at any time:

- **"Search our conversations for that discussion about database migrations."**
- **"What did we talk about last Tuesday?"**
- **"Find where I mentioned the deployment script."**

Stella searches across your messages and summaries, then reads the full details when she finds a match. Even if a conversation was compressed into a summary, she can expand it to recover the original details.

## Tips

- **Be explicit about what matters.** If you want Stella to remember something long-term, say so: "Remember that my production server is at 10.0.1.5."
- **Use constraints for safety rules.** Constraints are enforced more strictly than profile preferences.
- **Start new sessions when switching topics.** Each session has its own conversation history. Starting fresh keeps things focused, while your profile and constraints carry over automatically.
- **Search when you need old context.** Rather than keeping one infinitely long session, use search to pull in relevant history from past conversations.
