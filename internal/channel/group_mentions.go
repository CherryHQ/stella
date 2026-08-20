package channel

import (
	"context"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// This file owns every way a group message can come to name an agent. There are
// three ingress paths and they do not share a rule set by accident:
//
//   - platform ingest already receives structured mentions from the plugin, so
//     it resolves a platform identity (bot id, then bot display name) through
//     the bot registry and never looks at the text;
//   - agent-authored text and web-composed text are both scanned for @tokens,
//     and differ only in which tokens resolve: an agent may name a peer by
//     display name, web ingest matches member ids only.
//
// The two text paths share mentionScan below. The platform path has no text to
// scan, so it shares the file and not the scanner: forcing it through a common
// interface would add a layer that hides nothing.

// textMentionCutset is stripped from both ends of a word before its @ is read.
// Mentions are written inside prose ("ask @ada, then wait"), by an agent and by
// a human alike, so the trailing punctuation belongs to the sentence rather than
// to the name.
const textMentionCutset = "()[]{}:;,.!?"

// mentionScan is the shared matching logic behind both text entry points. Both
// scan prose the same way; resolve below is the only rule that differs.
type mentionScan struct {
	// trimCutset is cut from both ends of a word before the "@" prefix is read.
	trimCutset string
	// dedup emits one mention per agent. No consumer reads mention cardinality
	// -- triage asks whether an agent is named and whether any peer is named --
	// so repeats are noise in the stored envelope either way.
	dedup bool
	// resolve maps one @token to a member agent id. This is the rule that
	// actually differs: agent text accepts an agent id or a display name, web
	// ingest accepts an agent id only.
	resolve func(token string) (agentID string, ok bool)
}

// run appends every mention found in content to mentions and returns it. The
// caller supplies the starting slice so each entry point keeps its own empty
// value: those slices are serialized into the outbox envelope, where a nil and
// an empty list are not the same stored JSON.
func (s mentionScan) run(content string, mentions []pkgchannel.Mention) []pkgchannel.Mention {
	var seen map[string]struct{}
	if s.dedup {
		seen = make(map[string]struct{})
	}
	for word := range strings.FieldsSeq(content) {
		token, ok := strings.CutPrefix(strings.Trim(word, s.trimCutset), "@")
		if !ok {
			continue
		}
		agentID, ok := s.resolve(token)
		if !ok {
			continue
		}
		if s.dedup {
			if _, duplicate := seen[agentID]; duplicate {
				continue
			}
			seen[agentID] = struct{}{}
		}
		mentions = append(mentions, pkgchannel.Mention{Raw: "@" + token, AgentID: agentID})
	}
	return mentions
}

// parseGroupMentions keeps agent-authored outboxes self-contained. Web ingress
// already has member IDs; agent text may use either an ID or a display name.
func parseGroupMentions(ctx context.Context, q *sqlc.Queries, content string, members []sqlc.ChannelGroupMember) []pkgchannel.Mention {
	byToken := make(map[string]string, len(members)*2)
	for _, member := range members {
		byToken[member.AgentID] = member.AgentID
		if a, err := q.GetAgent(ctx, member.AgentID); err == nil && a.Name != "" {
			byToken[a.Name] = member.AgentID
		}
	}
	scan := mentionScan{
		trimCutset: textMentionCutset,
		dedup:      true,
		resolve: func(token string) (string, bool) {
			agentID, ok := byToken[token]
			return agentID, ok
		},
	}
	return scan.run(content, make([]pkgchannel.Mention, 0))
}

// parseWebMentions extracts @AgentID patterns from message text against the
// group's members. Web has no platform-level bot identity, so the AgentID is the
// mention target directly.
func parseWebMentions(content string, members []sqlc.ChannelGroupMember) []pkgchannel.Mention {
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}
	scan := mentionScan{
		trimCutset: textMentionCutset,
		dedup:      true,
		resolve: func(token string) (string, bool) {
			_, isMember := memberSet[token]
			return token, isMember
		},
	}
	return scan.run(content, nil)
}

// resolveMentionAgents fills Mention.AgentID from the bot registry alone, with
// no group context. It runs before the message text is composed so the stored
// text can name Stella agents; membership is enforced afterwards by
// clearNonMemberMentions, which is the only step that needs the group.
func (c *Coordinator) resolveMentionAgents(ctx context.Context, platform string, mentions []pkgchannel.Mention) {
	if c.botRegistry == nil || len(mentions) == 0 {
		return
	}
	log := slog.With("component", "group_dispatch", "platform", platform)
	for i := range mentions {
		if mentions[i].AgentID != "" {
			continue
		}
		channelID, ok := c.botRegistry.ChannelIDForBot(platform, mentions[i].PlatformID)
		if !ok {
			// A Feishu open_id is scoped to the receiving app, so a peer bot's id
			// never matches the id that peer registered for itself. The display
			// name is the only identity the platform shares across apps.
			channelID, ok = c.botRegistry.ChannelIDForBotName(platform, mentions[i].Raw)
		}
		if !ok {
			// The mentioned platform id is not a Stella bot, or the owning channel
			// never registered its identity (e.g. the bot open_id fetch failed at
			// startup). Left unresolved so triage still sees the wake instead of
			// suppressing the turn on a bad mention (#619).
			log.Debug("mention not in bot registry",
				"mention_raw", mentions[i].Raw, "platform_id", mentions[i].PlatformID)
			continue
		}
		ch, err := c.store.GetChannel(ctx, channelID)
		if err != nil || ch.AgentID == "" {
			log.Debug("mention channel lookup missed",
				"mention_raw", mentions[i].Raw, "platform_id", mentions[i].PlatformID,
				"channel_id", channelID, "channel_agent", ch.AgentID, "error", err)
			continue
		}
		mentions[i].AgentID = ch.AgentID
	}
}

// clearNonMemberMentions drops resolutions that point outside this group.
// members is the pre-fetched group member list (CR-005: avoids duplicate query).
func (c *Coordinator) clearNonMemberMentions(platform string, mentions []pkgchannel.Mention, members []GroupMember) {
	if len(mentions) == 0 {
		return
	}
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}
	log := slog.With("component", "group_dispatch", "platform", platform)
	for i := range mentions {
		if mentions[i].AgentID == "" {
			continue
		}
		if _, isMember := memberSet[mentions[i].AgentID]; !isMember {
			log.Debug("mention resolved to a non-member agent",
				"mention_raw", mentions[i].Raw, "channel_agent", mentions[i].AgentID)
			mentions[i].AgentID = ""
		}
	}
}

// rewriteMentionsToAgentNames renames resolved mentions to the Stella agent
// name. An agent knows its peers by their Stella names -- that is what the
// group prompt lists -- and platform display names are a different namespace
// entirely ("StellaDev" the Feishu app is "Stella" the agent). Leaving the
// platform name in the text is what makes an addressed agent unable to tell it
// was the one addressed.
func (c *Coordinator) rewriteMentionsToAgentNames(ctx context.Context, mentions []pkgchannel.Mention, content []ai.ContentBlock) []ai.ContentBlock {
	replacer := make([]string, 0, len(mentions)*2)
	for i := range mentions {
		if mentions[i].AgentID == "" || mentions[i].Raw == "" {
			continue
		}
		a, err := c.store.GetAgent(ctx, mentions[i].AgentID)
		if err != nil || a.Name == "" || a.Name == mentions[i].Raw {
			continue
		}
		replacer = append(replacer, "@"+mentions[i].Raw, "@"+a.Name)
	}
	if len(replacer) == 0 {
		return content
	}
	rep := strings.NewReplacer(replacer...)
	out := make([]ai.ContentBlock, len(content))
	copy(out, content)
	for i, block := range out {
		text, ok := block.(ai.TextContent)
		if !ok {
			continue
		}
		text.Text = rep.Replace(text.Text)
		out[i] = text
	}
	return out
}

// mentionsAgent answers whether a resolved mention names this agent.
func mentionsAgent(mentions []pkgchannel.Mention, agentID string) bool {
	for _, mention := range mentions {
		if mention.AgentID == agentID {
			return true
		}
	}
	return false
}

// resolvedMentions keeps only the mentions that landed on a Stella agent. An
// unresolved @ is platform noise, not an address.
func resolvedMentions(mentions []pkgchannel.Mention) []pkgchannel.Mention {
	resolved := make([]pkgchannel.Mention, 0, len(mentions))
	for _, mention := range mentions {
		if mention.AgentID != "" {
			resolved = append(resolved, mention)
		}
	}
	return resolved
}
