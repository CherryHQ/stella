package eventlog

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SystemParticipantName is how coordination rows are named to a model. It is a
// name, not an id, for the same reason every other participant has one.
const SystemParticipantName = "system"

// ParticipantNamer is the single source of the name a group participant is
// addressed by. Every group surface a model reads -- roster, transcript, the
// current trigger, nudge text -- renders participants through
// it, so the name the model sees is always the name it can @mention back.
//
// Resolution never fails: an unknown actor falls back to its raw id, which is
// worse to read but still stable.
//
// It is meant to live for one turn: names are cached so a transcript of N rows
// costs one lookup per distinct participant, and a rename is picked up by the
// next turn.
type ParticipantNamer struct {
	q *sqlc.Queries

	mu        sync.Mutex
	names     map[string]string
	platforms map[string]string
}

func NewParticipantNamer(q *sqlc.Queries) *ParticipantNamer {
	return &ParticipantNamer{q: q, names: map[string]string{}, platforms: map[string]string{}}
}

// Name resolves one participant. groupID is only needed for human actors, whose
// id is scoped to the group's platform.
func (n *ParticipantNamer) Name(ctx context.Context, groupID string, actorType, actorID string) string {
	if actorType == string(ActorSystem) {
		return SystemParticipantName
	}
	if n == nil || n.q == nil || actorID == "" {
		return actorID
	}
	key := groupID + "|" + actorType + "|" + actorID
	n.mu.Lock()
	cached, ok := n.names[key]
	n.mu.Unlock()
	if ok {
		return cached
	}
	name := n.resolve(ctx, groupID, actorType, actorID)
	n.mu.Lock()
	n.names[key] = name
	n.mu.Unlock()
	return name
}

func (n *ParticipantNamer) resolve(ctx context.Context, groupID, actorType, actorID string) string {
	if actorType == string(ActorAgent) {
		if agent, err := n.q.GetAgent(ctx, actorID); err == nil && agent.Name != "" {
			return agent.Name
		}
		return actorID
	}
	// Human. A platform sender is known by its channel identity; a Web sender's
	// actor id is the auth user id itself.
	if platform := n.platform(ctx, groupID); platform != "" && platform != "web" {
		if name, err := n.q.GetChannelIdentityName(ctx, sqlc.GetChannelIdentityNameParams{Platform: platform, ExternalID: actorID}); err == nil && name != "" {
			return name
		}
	}
	if user, err := n.q.GetAuthUser(ctx, actorID); err == nil && user.Name != "" {
		return user.Name
	}
	return actorID
}

func (n *ParticipantNamer) platform(ctx context.Context, groupID string) string {
	if groupID == "" {
		return ""
	}
	n.mu.Lock()
	platform, ok := n.platforms[groupID]
	n.mu.Unlock()
	if ok {
		return platform
	}
	if state, err := n.q.GetGroupStateByID(ctx, groupID); err == nil {
		platform = state.Platform
	}
	n.mu.Lock()
	n.platforms[groupID] = platform
	n.mu.Unlock()
	return platform
}

// Line renders one group message the way every group surface renders it: a seq,
// a participant, the text. Agents carry the "@" they are mentioned by so the
// label and the mention a model writes back are the same string.
//
// The trigger message of a turn uses this too: a peer post and a nudge reach
// the model as one more transcript line instead of as unattributed text.
func (n *ParticipantNamer) Line(ctx context.Context, groupID string, seq int64, actorType, actorID, content string) string {
	return fmt.Sprintf("[seq:%d %s]: %s", seq, n.Handle(ctx, groupID, actorType, actorID), content)
}

// Handle is Name with the "@" agents are addressed by.
func (n *ParticipantNamer) Handle(ctx context.Context, groupID string, actorType, actorID string) string {
	return HandleDisplayName(n.Name(ctx, groupID, actorType, actorID), actorType)
}

// HandleDisplayName applies the group-addressable agent prefix to a stored
// event-time name. Callers pass a live fallback only for legacy NULL snapshots.
func HandleDisplayName(name, actorType string) string {
	if actorType == string(ActorAgent) && !strings.HasPrefix(name, "@") {
		return "@" + name
	}
	return name
}
