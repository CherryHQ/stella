package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/grouptranscript"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// groupChatResolver runs the model turn behind a dispatch row: it serializes
// turns per (agent, group), re-checks the authority the row only claims, and
// renders the trigger the model actually reads.
//
// It is transport, not policy: nothing here decides whether a turn should run
// or what becomes of its reply. It owns the per-(agent, group) session queue,
// so aborting a running group turn goes through it.
type groupChatResolver struct {
	q     *sqlc.Queries
	coord *Coordinator
	queue *sessionQueue
}

func newGroupChatResolver(q *sqlc.Queries, coord *Coordinator) *groupChatResolver {
	return &groupChatResolver{q: q, coord: coord, queue: newSessionQueue()}
}

// abort stops the turn holding one session-queue slot. Idempotent: an idle slot
// has nothing to cancel.
func (r *groupChatResolver) abort(sessionKey string) bool {
	if r == nil {
		return false
	}
	return r.queue.Abort(sessionKey)
}

func (r *groupChatResolver) chatDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	stream, doneC, err := r.queue.Enqueue(ctx, sessionKey, func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		return r.chatDispatchUnqueued(qctx, row, message, state)
	})
	if err != nil {
		return nil, err
	}
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(doneC)
		defer close(out)
		for evt := range stream.Events {
			select {
			case out <- evt:
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range stream.Events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: stream.SessionID, OperationCheck: stream.OperationCheck}, nil
}

// errGroupTurnSuperseded reports that a dispatch row's trigger message sits at
// or below the agent's ingest cursor: a session rotation (or a completed later
// turn) already consumed it. The row is finished work, not a failure.
var errGroupTurnSuperseded = errors.New("group turn superseded by the agent's ingest cursor")

// errGroupNudgeMoot is checked only once the nudge owns the session-queue slot.
// A queued wake may post while the nudge waits; checking before Enqueue would
// race that post and announce a second turn that has already become pointless.
var errGroupNudgeMoot = errors.New("group nudge became moot")

func (r *groupChatResolver) chatDispatchUnqueued(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	if row.Kind == "nudge" {
		posted, err := r.q.AgentPostedSinceSeq(ctx, sqlc.AgentPostedSinceSeqParams{
			GroupID: row.GroupID, AgentID: row.AgentID, AfterSeq: row.TriggerSeq,
		})
		if err != nil {
			return nil, fmt.Errorf("recheck group nudge: %w", err)
		}
		if posted {
			return nil, errGroupNudgeMoot
		}
	}
	if r.coord == nil {
		return nil, errors.New("coordinator not configured")
	}
	// This runs inside the per-(agent,group) queue — the same queue that
	// serializes `/new` — so the cursor read cannot interleave with a rotation:
	// either the rotation committed first and its boundary is visible here, or
	// this turn runs first and the rotation waits. Checking outside the queue
	// would reopen exactly the race this closes: a dispatch row restarted after
	// a rotation would run a pre-reset trigger against the successor session.
	cursor, err := r.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  row.GroupID,
		Pipeline: memory.GroupIngestPipeline(row.AgentID),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No cursor yet: nothing has been consumed, the turn runs.
	case err != nil:
		return nil, fmt.Errorf("read group ingest cursor: %w", err)
	case message.Seq <= cursor.LastSeq:
		return nil, errGroupTurnSuperseded
	}
	ctx = memory.WithGroupMessageID(memory.WithGroupSeq(ctx, message.Seq), message.ID)
	content := r.triggerContent(ctx, row.GroupID, message)
	// The only surviving web/platform split, and it is about where a turn's
	// authority comes from, not about how the reply is delivered. A platform turn
	// re-checks the persisted channel binding it was routed through; a web group
	// has no such binding, so resolveWebGroupChat mints and re-checks the group
	// authority instead. Both then run buffered through the same session queue.
	if state.Platform == webGroupPlatform {
		return r.chatWeb(ctx, row, message, content)
	}
	// The dispatch row is routing state, not authority. Re-check the originating
	// persisted channel after this turn reaches the head of its execution queue.
	// resolveGroupChat separately re-checks that the agent itself is enabled.
	if err := ValidateGroupMembership(ctx, r.coord.store, state.Platform, row.AgentID, row.ReplyChannelID); err != nil {
		return nil, fmt.Errorf("validate queued group channel: %w", err)
	}
	rc, err := r.coord.resolveGroupChat(ctx, pkgchannel.IncomingMessage{
		Platform:  state.Platform,
		ChannelID: row.ReplyChannelID,
		SenderID:  message.ActorID,
		ChatID:    state.PlatformGroupID,
		IsGroup:   true,
		ThreadID:  state.PlatformThreadID,
		Content:   content,
		MessageID: nullStringValue(message.PlatformMessageID),
		ReplyTo:   nullStringValue(message.ReplyTo),
	}, row.GroupID, row.AgentID, row.ReplyChannelID)
	if err != nil {
		return nil, err
	}
	rc.CurrentSpeaker, rc.InputActor = groupMessageProvenance(message, rc.CurrentSpeaker)
	rc.GroupWake = memory.GroupWakeFromContext(ctx)
	return r.coord.chatWithRC(ctx, rc, content)
}

// groupMessageContentBlocks rebuilds the structured blocks persisted for a
// group message (images survive the event log via content_blocks), falling
// back to the plain-text projection for text-only or legacy rows.
func groupMessageContentBlocks(message sqlc.CtxGroupMessage) []ai.ContentBlock {
	if blocks, err := ai.UnmarshalContentBlocks(message.ContentBlocks); err == nil && blocks != nil {
		return blocks
	}
	return []ai.ContentBlock{ai.TextContent{Text: message.Content}}
}

// triggerContent renders the message that woke this turn the same way the
// injected transcript renders every other message: a seq and a participant
// name. A human question, a peer's post and a nudge all reach the model as one
// more labelled line, so "who is talking to me" never depends on which of the
// three woke the turn -- the case that had agents answering in each other's
// name. Attribution rides in the text because it must survive the prompt
// window; the structured actor envelope does not reach the model here.
func (r *groupChatResolver) triggerContent(ctx context.Context, groupID string, message sqlc.CtxGroupMessage) []ai.ContentBlock {
	blocks := groupMessageContentBlocks(message)
	namer := eventlog.NewParticipantNamer(r.q)
	name := namer.Name(ctx, groupID, message.ActorType, message.ActorID)
	if message.ActorDisplayName.Valid {
		name = message.ActorDisplayName.String
	}
	for i, block := range blocks {
		text, ok := block.(ai.TextContent)
		if !ok {
			continue
		}
		text.Text = grouptranscript.RenderGroupTranscriptLine(grouptranscript.GroupTranscriptEvent{
			Seq: message.Seq, ActorType: message.ActorType, DisplayName: name, Content: text.Text,
		})
		blocks[i] = text
	}
	for _, block := range blocks {
		if _, ok := block.(ai.TextContent); ok {
			return blocks
		}
	}
	// Image-only message: the label still has to arrive, as its own block.
	return append([]ai.ContentBlock{ai.TextContent{Text: grouptranscript.RenderGroupTranscriptLine(grouptranscript.GroupTranscriptEvent{
		Seq: message.Seq, ActorType: message.ActorType, DisplayName: name,
	})}}, blocks...)
}

// resolveWebGroupChat builds the group chat binding for an agent in a Web group.
// A persisted membership is not a standing execute grant: the authority is
// minted fresh for this exact group/member and re-authorized here, so service
// selection and the group authority live in exactly one place.
func (r *groupChatResolver) resolveWebGroupChat(ctx context.Context, groupID, agentID string) (*ResolvedChat, error) {
	if r == nil || r.coord == nil || r.coord.agentAccess == nil {
		return nil, ErrAgentAccessDenied
	}
	authority, err := agentaccess.GroupAgentAuthority(groupID, agentID)
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	if _, err := r.coord.agentAccess.Use(ctx, authority, agentID); err != nil {
		return nil, ErrAgentAccessDenied
	}
	svc := r.coord.serviceManager.GetService(agentID)
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

func (r *groupChatResolver) chatWeb(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, content []ai.ContentBlock) (*pkgchannel.ChatStream, error) {
	// Pool workers begin with a process context, unlike the historical HTTP
	// request path. Carry the confined group actor before any prompt/skill work.
	ctx = authz.WithAgentID(authz.WithGroupID(ctx, row.GroupID), row.AgentID)
	speaker := webGroupSpeaker(message)
	speaker, inputActor := groupMessageProvenance(message, speaker)
	// A persisted group membership is not an execute grant forever. The human
	// speaker is audit/personalization only; never borrow their private user
	// authority to execute a group turn. resolveWebGroupChat mints and re-checks
	// the group authority.
	rc, err := r.resolveWebGroupChat(ctx, row.GroupID, row.AgentID)
	if err != nil {
		return nil, err
	}
	info, err := rc.Service.ResolveChatChannelSession(ctx, rc.chatChannelRequest())
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	// CtxGroupMessage carries no display name; fill it best-effort from the auth
	// user so the prompt shows a real name instead of "Unknown". Fail-soft.
	if speaker.UserID != "" && speaker.DisplayName == "" && r.coord.auth != nil {
		if u, err := r.coord.auth.GetUser(ctx, speaker.UserID); err == nil && u.Name != "" {
			speaker.DisplayName = u.Name
		}
	}
	// The Web group turn does not go through ResolvedChat.Chat, so it attaches
	// the same durable chat-binding marker here; without it the group turn would
	// look like a Web send to tools that require a channel-backed chat.
	events := rc.Service.Chat(rc.withChatBinding(ctx), agent.ChatRequest{
		SessionID:      info.ID,
		UserID:         row.GroupID,
		AgentID:        row.AgentID,
		Kind:           session.KindChat,
		GroupID:        row.GroupID,
		Channel:        rc.Channel,
		Message:        agent.MessageContent(content),
		CurrentSpeaker: speaker,
		InputActor:     inputActor,
		GroupWake:      memory.GroupWakeFromContext(ctx),
		Authority:      rc.Authority,
	})
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		for evt := range events {
			select {
			case out <- convertEvent(evt):
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: info.ID}, nil
}

// webGroupSpeaker derives the per-turn speaker for a Web group dispatch. Web
// senders authenticate and SendGroupMessage persists the auth user id as
// actor_id, so it is a safe profile target — but only for a genuine human actor.
// Any other actor type or an empty id fails closed (zero speaker) so a malformed
// row never injects an arbitrary user's private profile.
func webGroupSpeaker(message sqlc.CtxGroupMessage) memory.CurrentSpeaker {
	if message.ActorType != string(eventlog.ActorHuman) || message.ActorID == "" {
		return memory.CurrentSpeaker{}
	}
	return memory.CurrentSpeaker{
		Platform:       webGroupPlatform,
		PlatformUserID: message.ActorID,
		UserID:         message.ActorID,
	}
}

// groupMessageProvenance makes coordination rows speakerless system input. The
// group-agent authority still controls which member can execute the resulting
// turn; this only prevents a system nudge from impersonating a human speaker.
func groupMessageProvenance(message sqlc.CtxGroupMessage, speaker memory.CurrentSpeaker) (memory.CurrentSpeaker, eventlog.MessageActor) {
	switch message.ActorType {
	case string(eventlog.ActorSystem):
		return memory.CurrentSpeaker{}, eventlog.MessageActor{Type: eventlog.ActorSystem, ID: message.ActorID}
	case string(eventlog.ActorHuman):
		return speaker, eventlog.MessageActor{}
	default:
		// A peer's post is not a speaker: <current_speaker> carries a human's
		// linked identity and profile target, and an agent has neither. The
		// trigger label is the whole provenance of an agent-authored wake.
		return memory.CurrentSpeaker{}, eventlog.MessageActor{}
	}
}
