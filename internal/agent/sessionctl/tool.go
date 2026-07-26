package sessionctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Action names for the session_control tool.
const (
	ActionRequestNew = "request_new"
	ActionConfirmNew = "confirm_new"
	ActionCompact    = "compact"

	// ToolName is the model-facing name of this tool.
	ToolName = "session_control"
)

// actionMeta describes a single tool action for schema/description generation.
type actionMeta struct {
	name string
	desc string
}

var toolActions = []actionMeta{
	{
		name: ActionRequestNew,
		desc: "Begin starting a fresh session (clears the conversation context). Performs no reset: it returns a nonce and you must then ask the user to confirm, in their language, and wait for their reply in a later message.",
	},
	{
		name: ActionConfirmNew,
		desc: "Finish starting a fresh session, using the nonce from request_new. Only call this after the user has explicitly agreed in a message that came AFTER you asked. Calling it in the same turn as request_new is rejected.",
	},
	{
		name: ActionCompact,
		desc: "Compress this session's history into summaries, keeping the same session. Non-destructive and needs no confirmation. Use it when the user wants a shorter context rather than a clean slate.",
	},
}

// BuildTool returns the session_control tool. services is looked up per call, so
// it may be a late-bound view of the agent pool.
func BuildTool(services agent.ServiceManager, store NonceStore) tools.Tool {
	return &sessionTool{services: services, store: store, ttl: DefaultTTL}
}

type sessionTool struct {
	services agent.ServiceManager
	store    NonceStore
	ttl      time.Duration
}

func (t *sessionTool) Definition() tools.Definition {
	var b strings.Builder
	b.WriteString("Control this conversation's session: start a fresh one, or compact the current one.\n\n" +
		"Starting fresh clears the working context. Nothing is deleted — past sessions stay searchable with the memory tool — but the reset is disruptive, so it takes two steps and the user's explicit agreement in between.\n\nActions:\n")
	for _, a := range toolActions {
		fmt.Fprintf(&b, "- %s: %s\n", a.name, a.desc)
	}
	names := make([]any, len(toolActions))
	for i, a := range toolActions {
		names[i] = a.name
	}
	return tools.Definition{
		Name:        ToolName,
		Description: b.String(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        names,
					"description": "The action to perform",
				},
				"nonce": map[string]any{
					"type":        "string",
					"description": "The nonce returned by request_new (required for confirm_new)",
				},
			},
			"required": []any{"action"},
		},
	}
}

func (t *sessionTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	turn, err := resolveChatTurn(ctx)
	if err != nil {
		return "", err
	}
	switch action {
	case ActionRequestNew:
		return t.execRequestNew(ctx, turn)
	case ActionConfirmNew:
		nonceID, _ := args["nonce"].(string)
		return t.execConfirmNew(ctx, turn, strings.TrimSpace(nonceID))
	case ActionCompact:
		return t.execCompact(ctx, turn)
	default:
		available := make([]string, len(toolActions))
		for i, a := range toolActions {
			available[i] = a.name
		}
		return "", fmt.Errorf("unknown action %q, available: %s", action, strings.Join(available, ", "))
	}
}

// execRequestNew records the pending reset and hands the model its half of the
// job: putting the question to the user. It deliberately changes nothing.
func (t *sessionTool) execRequestNew(ctx context.Context, turn chatTurn) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("starting a new session is not available on this deployment")
	}
	nonce := Nonce{
		ID:         uuid.Must(uuid.NewV7()).String(),
		SessionID:  turn.sessionID,
		BindingKey: turn.bindingKey(),
		ActorID:    turn.actorID,
		TurnMarker: turn.turnMarker,
		ExpiresAt:  time.Now().UTC().Add(t.ttl),
	}
	if err := t.store.Create(ctx, nonce); err != nil {
		return "", err
	}
	return fmt.Sprintf(`Nothing has been reset yet. nonce: %s

Now ask the user, in their own language, whether they want to start a fresh session. Tell them the current context will be cleared and that earlier messages stay searchable through your memory. Do NOT call confirm_new during this turn — a confirmation from the same turn is rejected, because the user has not answered yet.

Once they reply with a clear yes in a LATER message, call session_control with action=confirm_new and this nonce. If they say no or move on, just drop it. The nonce expires in %s.`,
		nonce.ID, t.ttl), nil
}

// execConfirmNew spends the nonce and rotates. Every check runs before the
// nonce is claimed, so a premature or misaddressed confirmation costs the user
// nothing: the pending question survives for the real answer.
func (t *sessionTool) execConfirmNew(ctx context.Context, turn chatTurn, nonceID string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("starting a new session is not available on this deployment")
	}
	if nonceID == "" {
		return "", fmt.Errorf("confirm_new needs the nonce from request_new")
	}
	nonce, err := t.store.Get(ctx, nonceID)
	switch {
	case errors.Is(err, ErrNonceNotFound):
		return "", errStaleNonce
	case err != nil:
		return "", err
	}
	if err := validateConfirm(turn, nonce, time.Now().UTC()); err != nil {
		return "", err
	}
	// Claim before rotating: the claim is the single-use gate, and a rotation
	// that ran first could be repeated by a concurrent confirmation.
	if _, err := t.store.Claim(ctx, nonce.ID); err != nil {
		if errors.Is(err, ErrNonceNotFound) {
			return "", errStaleNonce
		}
		return "", err
	}
	switch _, err := t.rotate(ctx, turn); {
	case err == nil:
		return "Started a fresh session. This turn still belongs to the previous one — tell the user their NEXT message begins the new session. Everything from before stays searchable through your memory.", nil
	case errors.Is(err, session.ErrStaleRotation):
		return "", errStaleNonce
	default:
		return "", fmt.Errorf("starting a new session failed: %w", err)
	}
}

var errStaleNonce = errors.New(
	"that confirmation is no longer valid — it expired, was already used, or this chat has since moved to a different session. " +
		"Nothing was reset. Tell the user, and call request_new again if they still want a fresh session")

// validateConfirm decides whether a confirmation may spend this nonce. The
// checks together stand in for the one thing the server cannot judge — whether
// the user actually said yes — by proving that a real user turn, from the right
// person, in the right chat, answered within the window.
func validateConfirm(turn chatTurn, nonce Nonce, now time.Time) error {
	if nonce.Used() || nonce.Expired(now) {
		return errStaleNonce
	}
	// A nonce belongs to the chat that issued it. A different chat (or a
	// different session in the same chat, after some other `/new` landed) has
	// nothing this nonce can authorize.
	if nonce.BindingKey != turn.bindingKey() || nonce.SessionID != turn.sessionID {
		return errStaleNonce
	}
	if nonce.TurnMarker == turn.turnMarker {
		return errors.New(
			"rejected: confirm_new ran in the same turn that requested it, so the user has not answered yet. " +
				"Ask them now, and call confirm_new only after they reply in a later message")
	}
	if nonce.ActorID != turn.actorID {
		return errors.New(
			"rejected: only the person who asked for a fresh session can confirm it, and this message is from someone else. " +
				"Tell them to ask for the reset themselves if they want one")
	}
	return nil
}

// rotate moves the chat onto a successor session through the same authorized
// compare-and-rotate path `/new` uses. The observed session is passed as the
// expected one, so a rotation that raced another cannot archive the successor.
func (t *sessionTool) rotate(ctx context.Context, turn chatTurn) (session.Info, error) {
	svc, err := t.serviceFor(turn.agentID)
	if err != nil {
		return session.Info{}, err
	}
	authority, err := turn.authority()
	if err != nil {
		return session.Info{}, err
	}
	if turn.groupID == "" && turn.binding.Main {
		return svc.RotateMainSession(ctx, authority, turn.userID, turn.agentID, turn.sessionID)
	}
	return svc.RotateChatChannelSession(ctx, turn.chatChannelRequest(authority), turn.sessionID)
}

// execCompact compresses the current session in place. It needs no confirmation
// because nothing is lost: the same session keeps going with a shorter context.
func (t *sessionTool) execCompact(ctx context.Context, turn chatTurn) (string, error) {
	svc, err := t.serviceFor(turn.agentID)
	if err != nil {
		return "", err
	}
	authority, err := turn.authority()
	if err != nil {
		return "", err
	}
	var info session.Info
	if turn.groupID == "" && turn.binding.Main {
		info, err = svc.ResolveMainSessionForUse(ctx, authority, turn.userID, turn.agentID)
	} else {
		info, err = svc.ResolveChatChannelSessionForUse(ctx, turn.chatChannelRequest(authority))
	}
	if err != nil {
		return "", fmt.Errorf("compaction failed: %w", err)
	}
	summary, err := svc.CompactAuthorizedSession(ctx, info)
	if err != nil {
		if errors.Is(err, agent.ErrGroupCompactionUnsupported) {
			return "", errors.New(pkgchannel.GroupCompactUnsupportedMessage +
				" Nothing was compacted; relay this to the group and offer a fresh session instead if they want a clean context")
		}
		return "", fmt.Errorf("compaction failed: %w", err)
	}
	return "Session compacted, same session: " + summary, nil
}

func (t *sessionTool) serviceFor(agentID string) (*agent.Service, error) {
	if t.services == nil {
		return nil, fmt.Errorf("session control is not available on this deployment")
	}
	svc := t.services.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}
	return svc, nil
}

// authority mints this turn's capability from the identity the runtime
// established, never from tool arguments — a group turn gets the confined
// group/agent capability, a DM the owner's worker capability.
func (t chatTurn) authority() (authz.Authority, error) {
	if t.groupID != "" {
		return agentaccess.GroupAgentAuthority(t.groupID, t.agentID)
	}
	return agentaccess.WorkerAgentAuthority(t.userID, t.agentID)
}

func (t chatTurn) chatChannelRequest(authority authz.Authority) agent.ChatChannelRequest {
	userID := t.userID
	if t.groupID != "" {
		userID = t.groupID
	}
	return agent.ChatChannelRequest{
		Authority:  authority,
		UserID:     userID,
		GroupID:    t.groupID,
		AgentID:    t.agentID,
		Channel:    session.Channel(t.binding.Channel),
		SessionKey: t.binding.SessionKey,
	}
}
