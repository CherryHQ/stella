package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// errGroupRouteBusy means this outbox is valid but its sequence predecessor or
// current claimant still owns the ordered classification slot. The caller
// requeues the outbox explicitly; it must not mark the route complete or drop
// the message merely because another group member is busy.
var errGroupRouteBusy = errors.New("group route busy")

const (
	groupRouteActionWake  = "wake"
	groupRouteActionNudge = "nudge"
	groupRouteActionSkip  = "skip"
	groupRouteActionBusy  = "busy"
)

// groupRouteDecision is the durable classification result. It records which
// member was admitted to the channel FIFO, while keeping model output and
// execution authority out of the route row.
type groupRouteDecision struct {
	AgentID        string `json:"agent_id"`
	ReplyChannelID string `json:"reply_channel_id"`
	Action         string `json:"action"`
	Reason         string `json:"reason"`
	DispatchID     string `json:"dispatch_id,omitempty"`
}

func routeClaimToken() pgtype.Text {
	return pgtype.Text{String: uuid.Must(uuid.NewV7()).String(), Valid: true}
}

func (d *GroupDispatcher) materializeGroupRouteTx(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	if d.db == nil {
		return errors.New("group route requires dispatcher db")
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group route: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := d.q.WithTx(tx)
	message, err := q.GetGroupMessage(ctx, outbox.GroupMessageID)
	if err != nil {
		return fmt.Errorf("get group route message: %w", err)
	}
	route, err := q.GetChannelGroupRouteByMessage(ctx, message.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		route, err = q.CreateChannelGroupRoute(ctx, sqlc.CreateChannelGroupRouteParams{
			ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: message.ID,
			GroupID: message.GroupID, GroupSeq: message.Seq,
		})
	}
	if err != nil {
		return fmt.Errorf("ensure group route: %w", err)
	}
	if route.Status == "completed" {
		return nil
	}
	if route.Status == "claimed" && route.ClaimExpiresAt.Valid && route.ClaimExpiresAt.Time.After(time.Now().UTC()) {
		return errGroupRouteBusy
	}
	claimed, err := q.ClaimChannelGroupRoute(ctx, sqlc.ClaimChannelGroupRouteParams{
		ID: route.ID, ClaimToken: routeClaimToken(), LeaseSeconds: routeLeaseSeconds(d.leaseDuration),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errGroupRouteBusy
	}
	if err != nil {
		return fmt.Errorf("claim group route: %w", err)
	}
	decisions, err := d.classifyGroupRoute(ctx, q, claimed, message, outbox)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(decisions)
	if err != nil {
		return fmt.Errorf("encode group route decisions: %w", err)
	}
	updated, err := q.CompleteChannelGroupRoute(ctx, sqlc.CompleteChannelGroupRouteParams{
		ID: claimed.ID, ClaimToken: claimed.ClaimToken, Decisions: encoded,
	})
	if err != nil {
		return fmt.Errorf("complete group route: %w", err)
	}
	if updated == 0 {
		return errGroupRouteBusy
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group route: %w", err)
	}
	committed = true
	return nil
}

func routeLeaseSeconds(duration time.Duration) int32 {
	seconds := min(max(duration/time.Second, 1), time.Duration(^uint32(0)>>1))
	return int32(seconds)
}

func (d *GroupDispatcher) classifyGroupRoute(ctx context.Context, q *sqlc.Queries, route sqlc.ChannelGroupRoute, message sqlc.CtxGroupMessage, outbox sqlc.CtxGroupOutbox) ([]groupRouteDecision, error) {
	state, err := q.GetGroupStateByID(ctx, route.GroupID)
	if err != nil {
		return nil, fmt.Errorf("get group route state: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, route.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list group route members: %w", err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		return nil, fmt.Errorf("decode group route envelope: %w", err)
	}
	decisions := make([]groupRouteDecision, 0, len(members))
	for _, member := range members {
		decision := groupRouteDecision{AgentID: member.AgentID, ReplyChannelID: member.ReplyChannelID}
		if message.ActorType == string(eventlog.ActorAgent) && member.AgentID == message.ActorID {
			decision.Action, decision.Reason = groupRouteActionSkip, "author"
			decisions = append(decisions, decision)
			continue
		}
		if envelope.NudgeTarget != "" && envelope.NudgeTarget != member.AgentID {
			decision.Action, decision.Reason = groupRouteActionSkip, "nudge_target"
			decisions = append(decisions, decision)
			continue
		}
		row := sqlc.CtxGroupDispatch{
			ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: message.ID,
			GroupID: route.GroupID, AgentID: member.AgentID,
			ReplyChannelID: member.ReplyChannelID, Kind: groupRouteActionWake,
			TriggerSeq: message.Seq,
		}
		if envelope.NudgeTarget != "" {
			row.Kind = groupRouteActionNudge
		}
		act, reason, degraded := d.triageWakeWithQueries(ctx, q, row, message, state, envelope)
		if degraded {
			// Keep the route claimed and let its outbox retry. Persisting a false
			// skip would permanently lose a responder on a transient DB read.
			return nil, fmt.Errorf("%w: agent %s: %s", errGroupRouteBusy, member.AgentID, reason)
		}
		if !act {
			decision.Action, decision.Reason = groupRouteActionSkip, reason
			decisions = append(decisions, decision)
			continue
		}
		decision.Action, decision.Reason = row.Kind, reason
		chatKey := state.PlatformGroupID
		if chatKey == "" {
			// Web groups use their canonical group id as the physical chat key;
			// platform groups normally carry the provider's chat id above.
			chatKey = route.GroupID
		}
		item, _, err := EnqueueGroupResponderTx(ctx, q, GroupResponderEnqueue{
			RouteID: route.ID, MessageID: message.ID, GroupID: route.GroupID,
			AgentID: member.AgentID, ChannelID: member.ReplyChannelID,
			Platform: state.Platform, ChatKey: chatKey, ThreadKey: state.PlatformThreadID,
			Seq: message.Seq, Kind: row.Kind, Decision: decision.Action, Reason: reason,
		})
		if err != nil {
			return nil, fmt.Errorf("materialize group route responder %s: %w", member.AgentID, err)
		}
		decision.DispatchID = item.ID
		decisions = append(decisions, decision)
	}
	return decisions, nil
}
