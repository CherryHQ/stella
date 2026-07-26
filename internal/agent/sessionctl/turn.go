package sessionctl

import (
	"context"
	"errors"
	"strconv"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// errNotChannelBacked is the refusal for every turn that is not a chat a user
// can keep talking in. The text is the model's whole explanation, so it says
// what to do instead rather than what went wrong internally.
var errNotChannelBacked = errors.New(
	"session_control is only available in a chat channel (a direct message or a group). " +
		"This conversation is not one, so there is no channel binding to move onto a fresh session. " +
		"Tell the user to start a new session from the interface they are using instead")

// chatTurn is everything session_control is allowed to know about the turn it
// runs in. Every field comes from the runtime's own context — never from tool
// arguments — so the model cannot name a session, a chat, or a speaker other
// than its own (the RunDelegateSession contract).
type chatTurn struct {
	sessionID  string
	agentID    string
	userID     string // session owner; empty in a group turn, where the group owns the session
	groupID    string
	actorID    string // the speaker who must confirm; empty in a DM
	turnMarker string
	binding    agentctx.ChatBinding
}

// resolveChatTurn admits only turns backed by a durable chat binding and fails
// closed on everything else.
//
// The binding marker is attached by the chat channel adapters alone, so Web/API
// sends, webhook ingress, and scheduler/task/delegate runs are excluded by its
// absence rather than by a list this code has to keep in sync. That matters
// because the session row cannot answer the question: the Web UI can open the
// very same main session a DM is pinned to, and rotating under an open tab
// would break it while the Web UI already has its own "new session" control.
func resolveChatTurn(ctx context.Context) (chatTurn, error) {
	binding, ok := agentctx.ChatBindingFromContext(ctx)
	if !ok {
		return chatTurn{}, errNotChannelBacked
	}
	turn := chatTurn{
		sessionID: memory.SessionIDFromContext(ctx),
		agentID:   authz.AgentIDFromContext(ctx),
		userID:    authz.UserIDFromContext(ctx),
		groupID:   authz.GroupIDFromContext(ctx),
		binding:   binding,
	}
	if turn.sessionID == "" || turn.agentID == "" {
		return chatTurn{}, errNotChannelBacked
	}
	if turn.groupID == "" && turn.userID == "" {
		return chatTurn{}, errNotChannelBacked
	}
	if turn.groupID != "" {
		actor, err := groupActorID(ctx)
		if err != nil {
			return chatTurn{}, err
		}
		turn.actorID = actor
	}
	marker, err := turnMarker(ctx)
	if err != nil {
		return chatTurn{}, err
	}
	turn.turnMarker = marker
	return turn, nil
}

// groupActorID identifies the human whose confirmation counts in a group. It
// prefers the platform sender id because it exists for unlinked members too,
// where the resolved auth user id does not. A group turn with no identifiable
// speaker cannot bind a confirmation to anyone, so it gets no reset at all.
func groupActorID(ctx context.Context) (string, error) {
	speaker, _ := memory.CurrentSpeakerFromContext(ctx)
	switch {
	case speaker.PlatformUserID != "":
		return speaker.Platform + ":" + speaker.PlatformUserID, nil
	case speaker.UserID != "":
		return "user:" + speaker.UserID, nil
	default:
		return "", errUnidentifiedGroupSender
	}
}

var errUnidentifiedGroupSender = errors.New(
	"session_control is unavailable for this group message: its sender could not be identified, " +
		"so there is nobody whose confirmation to reset the group's shared context would count")

// turnMarker names the current turn well enough to tell it apart from the turn
// that issued a nonce — the check that proves a real user message arrived in
// between.
//
// Group turns use the event-log seq of the message that triggered them. It is
// the only marker that survives the durable dispatcher retrying a message: a
// retry is the same user message and must not count as an answer. Everything
// else uses the runtime's per-turn id, which is unique per Chat call; DM turns
// have no retry path, and their per-session serialization means a different turn
// is always a different user message.
//
// Message counts were the obvious alternative and are wrong: the runtime
// persists assistant and tool messages as the turn runs, so a count taken at
// request time has already grown by the time the same turn calls confirm.
func turnMarker(ctx context.Context) (string, error) {
	if seq := memory.GroupSeqFromContext(ctx); seq > 0 {
		return "gseq:" + strconv.FormatInt(seq, 10), nil
	}
	if id := agentctx.TurnIDFromContext(ctx); id != "" {
		return "turn:" + id, nil
	}
	return "", errNotChannelBacked
}

// bindingKey is the durable identity of the chat this turn belongs to. A nonce
// is spendable only in the chat it was issued in, and this string is how the
// store compares two chats without knowing how either is shaped.
func (t chatTurn) bindingKey() string {
	switch {
	case t.groupID != "":
		// A group's channel varies with the reply channel a message arrives
		// through, so the group itself is the stable half of the binding.
		return "group:" + t.groupID + ":" + t.agentID
	case t.binding.Main:
		return "main:" + t.userID + ":" + t.agentID
	default:
		return "channel:" + t.userID + ":" + t.agentID + ":" + t.binding.Channel
	}
}
