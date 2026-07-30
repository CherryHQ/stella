package channel

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// Group `/new` rotates one agent's group session onto a fresh one.
//
// Two properties shape this file:
//
//   - The command must be intercepted before the group event log is written, in
//     both group entry points (platform ingest and the Web group send), so the
//     `/new` text never lands in any agent's assembled context.
//   - Rotation is per agent. Each agent in a group keeps its own session
//     (BuildGroupSessionKey), so a group with several agents requires
//     `/new @agent`; a bare `/new` there returns a usage reply rather than
//     resetting everyone's context on an ambiguous command.
//
// Ordering matches the DM path in spirit but not in mechanism: group turns are
// serialized by the durable dispatcher's own per-(agent,group) queue, not by the
// coordinator's, so rotation is enqueued there and therefore runs after any
// in-flight group turn for that agent instead of racing it.

// RotateGroupSession starts a fresh session for one agent in a Web group and
// returns the user-facing reply. It runs in the same per-(agent,group) queue as
// that agent's group turns, so a rotation waits for an in-flight turn instead of
// stranding its reply in a session the group has already left.
func (d *GroupDispatcher) RotateGroupSession(ctx context.Context, groupID, agentID string) (string, error) {
	rc, err := d.resolveWebGroupChat(ctx, groupID, agentID)
	if err != nil {
		return "", err
	}
	return d.rotateGroupChat(ctx, rc), nil
}

// resolveWebGroupChat builds the group chat binding for an agent in a Web group.
// A persisted membership is not a standing execute grant: the authority is
// minted fresh for this exact group/member and re-authorized here. Both the Web
// group turn and the Web group `/new` enter through it, so service selection and
// the group authority live in exactly one place.
func (d *GroupDispatcher) resolveWebGroupChat(ctx context.Context, groupID, agentID string) (*ResolvedChat, error) {
	if d == nil || d.coord == nil || d.coord.agentAccess == nil {
		return nil, ErrAgentAccessDenied
	}
	authority, err := agentaccess.GroupAgentAuthority(groupID, agentID)
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	if _, err := d.coord.agentAccess.Use(ctx, authority, agentID); err != nil {
		return nil, ErrAgentAccessDenied
	}
	svc := d.coord.serviceManager.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}
	return &ResolvedChat{
		Service:    svc,
		AgentID:    agentID,
		SessionKey: agent.BuildGroupSessionKey(agentID, groupID),
		Channel:    session.Channel("group:" + groupID),
		GroupID:    groupID,
		Authority:  authority,
	}, nil
}

// rotateGroupChat resolves the session the command names, then rotates it from
// inside the agent's group turn queue. Resolving first (outside the queue) makes
// a duplicate `/new` behind this one name a session that is already archived, so
// it reports the reset as done instead of resetting a second time.
func (d *GroupDispatcher) rotateGroupChat(ctx context.Context, rc *ResolvedChat) string {
	current, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	run := func(fn func(context.Context) error) error { return fn(ctx) }
	if d != nil && d.queue != nil {
		run = func(fn func(context.Context) error) error {
			return d.queue.EnqueueControl(ctx, rc.SessionKey, fn)
		}
	}
	var reply string
	if err := run(func(qctx context.Context) error {
		reply = NewSessionReply(qctx, rc, current.ID)
		return nil
	}); err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	return reply
}

// handleGroupNewSessionCommand answers a platform group's `/new`. It runs before
// the event-log append, so the command itself never enters group context.
func (c *Coordinator) handleGroupNewSessionCommand(ctx context.Context, msg pkgchannel.IncomingMessage, args string) string {
	if c.groupResolver == nil || c.memberLister == nil {
		return pkgchannel.GroupNewSessionUnavailableMessage
	}
	groupID, err := c.groupResolver.ResolveGroupID(ctx, msg.Platform, msg.ChatID, msg.ThreadID)
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	members, err := c.memberLister.ListGroupMembers(ctx, groupID)
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}

	// A platform mention carries a bot's platform id; resolve it to an agent the
	// same way a normal group turn does before matching it against the roster.
	c.resolveMentionAgentsWithMembers(ctx, groupID, msg.Platform, msg.Mentions, members)
	agentIDs := memberAgentIDs(members)
	target, usage := groupNewTarget(firstMentionedAgent(msg.Mentions), args, agentIDs)
	if target == "" {
		return usage
	}

	rc, err := c.resolveGroupChat(ctx, msg, groupID, target, findMemberReplyChannel(members, target))
	if err != nil {
		return fmt.Sprintf("Starting a new session failed: %v", err)
	}
	return c.groupDispatcher.rotateGroupChat(ctx, rc)
}

// groupNewTarget picks the agent whose group session a `/new` rotates. A group
// with exactly one agent needs no target; anything ambiguous returns a usage
// reply instead, because resetting every agent's context on an unclear command
// is destructive by default.
func groupNewTarget(mentioned, args string, agentIDs []string) (target string, usage string) {
	if len(agentIDs) == 0 {
		return "", pkgchannel.GroupNewSessionNoAgentsMessage
	}
	named := mentioned
	if named == "" {
		named = namedAgentInArgs(args)
	}
	if named != "" {
		if !slices.Contains(agentIDs, named) {
			return "", pkgchannel.GroupNewSessionUsageMessage(agentIDs)
		}
		return named, ""
	}
	if len(agentIDs) == 1 {
		return agentIDs[0], ""
	}
	return "", pkgchannel.GroupNewSessionUsageMessage(agentIDs)
}

// namedAgentInArgs matches a literal `@agent` argument against the roster. It
// covers platforms whose mention never resolves to a registered bot identity and
// the Web group, where the agent id is the mention target directly.
func namedAgentInArgs(args string) string {
	for field := range strings.FieldsSeq(args) {
		name, ok := strings.CutPrefix(field, "@")
		if !ok {
			continue
		}
		// A named-but-unknown agent must not silently fall through to the
		// single-member shortcut; return it so the caller replies with the roster.
		return name
	}
	return ""
}

func memberAgentIDs(members []GroupMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.AgentID)
	}
	return out
}
